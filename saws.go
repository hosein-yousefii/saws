package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Accounts      map[string]string `yaml:"accounts"`
	CommonRegions []string          `yaml:"common_regions"`
	Roles         map[string]string `yaml:"roles"`
}

var accounts map[string]string
var commonRegions []string
var roles map[string]string

const (
	baseProfileForAssume   = "default"
	fallbackRegion         = "eu-west-1"
	sessionDurationSeconds = 3600
	configFileName         = "saws-config.yaml"
	awsConfigDir           = ".aws"
)

// Config Loading Functions
func loadConfig(filePath string) (*AppConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file '%s': %w", filePath, err)
	}
	var config AppConfig
	config.Accounts = make(map[string]string)
	config.Roles = make(map[string]string)
	config.CommonRegions = []string{}
	err = yaml.Unmarshal(data, &config)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML config file '%s': %w", filePath, err)
	}
	if len(config.Accounts) == 0 {
		return nil, fmt.Errorf("config validation failed: 'accounts' map cannot be empty in '%s'", filePath)
	}
	if len(config.CommonRegions) == 0 {
		return nil, fmt.Errorf("config validation failed: 'common_regions' list cannot be empty in '%s'", filePath)
	}
	log.Printf("Successfully loaded configuration: %d accounts, %d regions, %d roles defined in, %s", len(config.Accounts), len(config.CommonRegions), len(config.Roles), filePath)
	return &config, nil
}
func findConfigPath(configFileOverride string) (string, error) {
	if configFileOverride != "" {
		if strings.HasPrefix(configFileOverride, "~") {
			homeDir, errHome := os.UserHomeDir()
			if errHome == nil {
				configFileOverride = filepath.Join(homeDir, configFileOverride[1:])
			} else {
				log.Printf("Warning: Could not expand '~' in config path: %v", errHome)
			}
		}
		if _, err := os.Stat(configFileOverride); err == nil {
			log.Printf("Using specified config file: %s", configFileOverride)
			return configFileOverride, nil
		}
		return "", fmt.Errorf("specified config file '%s' not found", configFileOverride)
	}
	homeDir, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(homeDir, awsConfigDir, configFileName)
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
	} else {
		log.Printf("Warning: Could not determine home directory: %v. Cannot check default ~/.aws location.", err)
	}
	configPathLocal := configFileName
	if _, err := os.Stat(configPathLocal); err == nil {
		return configPathLocal, nil
	}
	return "", fmt.Errorf("configuration file ('%s') not found in standard locations (~/%s/%s, ./%s) and no -config flag provided", configFileName, awsConfigDir, configFileName, configFileName)
}

// Usage Function
func usage() {
	toolName := "saws"
	fmt.Fprintf(os.Stderr, `Usage:
  %s -r <role> -c <cmd> [-regions <region1,...>] [-a | -s <selector>] [-config <path>] (Execute Command Mode)
  %s -e [-config <path>]                                                              (Setup Environment Mode)

Configuration:
  Requires a %s file containing accounts and common_regions.
  Searches in ~/%s/%s, ./%s, or path specified by -config.
  Optionally, the config file can contain a 'roles' map (friendly_name -> iam_role_name) for selection in -e mode.

Options for Execute Command Mode:
`, toolName, toolName, configFileName, awsConfigDir, configFileName, configFileName)
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		if f.Name == "e" || f.Name == "h" || f.Name == "config" {
			return
		}
		usage := f.Usage
		if f.Name == "r" {
			usage = "Mandatory. IAM role name to assume."
		}
		fmt.Fprintf(os.Stderr, "  -%s %s\n", f.Name, usage)
	})
	fmt.Fprintln(os.Stderr, "\nOptions for Setup Environment Mode:")
	fmt.Fprintf(os.Stderr, "  -%s %s\n", "e", "Enable interactive mode to select account/role/region and output 'export' commands.")
	fmt.Fprintf(os.Stderr, "  -%s %s\n", "config", flag.Lookup("config").Usage)
	fmt.Fprintln(os.Stderr, "\nSelector (-s) can be:")
	fmt.Fprintln(os.Stderr, "   - A single name (e.g., 'company-infra-dev')")
	fmt.Fprintln(os.Stderr, "   - A pattern with wildcard * (e.g., 'company-infra*')")
	fmt.Fprintln(os.Stderr, "   - Multiple names/patterns separated by space (enclose in quotes)")
	fmt.Fprintln(os.Stderr, "\nNotes:")
	fmt.Fprintln(os.Stderr, " - Command Mode: Requires -r, -c, and either -a or -s.")
	fmt.Fprintln(os.Stderr, " - Environment Mode (-e): Ignores -c, -regions, -a, -s, -r.")
	fmt.Fprintln(os.Stderr, " - If 'roles' section exists in config, -e mode presents it for selection, otherwise prompts for manual input.")
	fmt.Fprintln(os.Stderr, " - If -regions is omitted in Command Mode, uses default region from AWS config/env or falls back to '"+fallbackRegion+"'.")
	fmt.Fprintln(os.Stderr, " - Requires AWS CLI (for Command Mode) to be installed.")
	fmt.Fprintln(os.Stderr, "\nExamples:")
	fmt.Fprintln(os.Stderr, " - saws -r Company-admin -c \"aws s3 ls\" -s \"company-infra-* company-base-test\" -regions \"eu-west-1, us-east-1\"")
	fmt.Fprintln(os.Stderr, " - saws -e")
	os.Exit(1)
}

// Helper Function: Assume Role
func assumeRole(ctx context.Context, baseCfg aws.Config, accountID, roleToAssume, sessionNameSuffix string) (*types.Credentials, error) {
	stsClient := sts.NewFromConfig(baseCfg)
	roleArn := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleToAssume)
	sessionName := fmt.Sprintf("%s-%s-%d", sessionNameSuffix, strings.ReplaceAll(roleToAssume, "/", "-"), os.Getpid())
	if len(sessionName) > 64 {
		sessionName = sessionName[:64]
	}
	assumeRoleInput := &sts.AssumeRoleInput{RoleArn: aws.String(roleArn), RoleSessionName: aws.String(sessionName), DurationSeconds: aws.Int32(sessionDurationSeconds)}
	assumeRoleOutput, err := stsClient.AssumeRole(ctx, assumeRoleInput)
	if err != nil {
		return nil, fmt.Errorf("sts:AssumeRole call failed for role %s: %w", roleArn, err)
	}
	if assumeRoleOutput.Credentials == nil || assumeRoleOutput.Credentials.AccessKeyId == nil || assumeRoleOutput.Credentials.SecretAccessKey == nil || assumeRoleOutput.Credentials.SessionToken == nil {
		return nil, fmt.Errorf("assume role response for %s did not contain valid credentials", roleArn)
	}
	return assumeRoleOutput.Credentials, nil
}

// Function for Interactive Environment Setup
func setupEnvironmentInteractive(ctx context.Context) error {
	if len(accounts) == 0 {
		return errors.New("internal error: accounts map is empty (check config load)")
	}
	accountNames := make([]string, 0, len(accounts))
	for name := range accounts {
		accountNames = append(accountNames, name)
	}
	sort.Strings(accountNames)
	selectedAccountName := ""
	promptAccount := &survey.Select{Message: "Choose an AWS Account:", Options: accountNames, PageSize: 15}
	err := survey.AskOne(promptAccount, &selectedAccountName, survey.WithValidator(survey.Required))
	if err != nil {
		return fmt.Errorf("account selection failed: %w", err)
	}
	selectedAccountID := accounts[selectedAccountName]
	baseCfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(baseProfileForAssume), config.WithRegion(fallbackRegion))
	if err != nil {
		return fmt.Errorf("failed to load base AWS config profile '%s': %w", baseProfileForAssume, err)
	}
	selectedRoleName := ""
	if len(roles) > 0 {
		log.Println("Roles found in config file. Presenting selection...")
		friendlyRoleNames := make([]string, 0, len(roles))
		for friendlyName := range roles {
			friendlyRoleNames = append(friendlyRoleNames, friendlyName)
		}
		sort.Strings(friendlyRoleNames)
		chosenFriendlyName := ""
		promptRoleSelect := &survey.Select{Message: "Choose Role to Assume:", Options: friendlyRoleNames, PageSize: 15}
		err = survey.AskOne(promptRoleSelect, &chosenFriendlyName, survey.WithValidator(survey.Required))
		if err != nil {
			return fmt.Errorf("role selection failed: %w", err)
		}
		selectedRoleName = roles[chosenFriendlyName]
		log.Printf("Selected friendly role '%s' -> actual role '%s'.", chosenFriendlyName, selectedRoleName)
	} else {
		log.Println("No 'roles' section found in config file. Please provide the role name manually.")
		promptManualRole := &survey.Input{Message: "Enter the exact IAM Role Name to Assume:"}
		err = survey.AskOne(promptManualRole, &selectedRoleName, survey.WithValidator(survey.Required))
		if err != nil {
			return fmt.Errorf("manual role input failed: %w", err)
		}
	}
	if len(commonRegions) == 0 {
		return errors.New("internal error: commonRegions list is empty (check config load)")
	}
	defaultRegionChoice := fallbackRegion
	tempCfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(baseProfileForAssume))
	if err == nil && tempCfg.Region != "" {
		defaultRegionChoice = tempCfg.Region
	}
	foundDefaultInList := false
	for _, r := range commonRegions {
		if r == defaultRegionChoice {
			foundDefaultInList = true
			break
		}
	}
	if !foundDefaultInList && len(commonRegions) > 0 {
		defaultRegionChoice = commonRegions[0]
	}
	selectedRegion := ""
	promptRegion := &survey.Select{Message: "Choose AWS Region:", Options: commonRegions, Default: defaultRegionChoice}
	err = survey.AskOne(promptRegion, &selectedRegion, survey.WithValidator(survey.Required))
	if err != nil {
		return fmt.Errorf("region selection failed: %w", err)
	}
	log.Printf("Attempting to assume final role '%s' in account '%s' (%s) for region '%s'...", selectedRoleName, selectedAccountName, selectedAccountID, selectedRegion)
	finalCreds, err := assumeRole(ctx, baseCfg, selectedAccountID, selectedRoleName, "InteractiveSession")
	if err != nil {
		return fmt.Errorf("failed to assume role '%s': %w", selectedRoleName, err)
	}
	fmt.Printf("export AWS_ACCESS_KEY_ID='%s'\n", *finalCreds.AccessKeyId)
	fmt.Printf("export AWS_SECRET_ACCESS_KEY='%s'\n", *finalCreds.SecretAccessKey)
	fmt.Printf("export AWS_SESSION_TOKEN='%s'\n", *finalCreds.SessionToken)
	fmt.Printf("export AWS_REGION='%s'\n", selectedRegion)
	fmt.Printf("export AWS_DEFAULT_REGION='%s'\n", selectedRegion)
	fmt.Println("unset AWS_PROFILE")
	expirationTime := "N/A"
	if finalCreds.Expiration != nil {
		expirationTime = finalCreds.Expiration.Local().Format(time.RFC1123)
	}
	log.Printf("Success! AWS credentials exported for role '%s' in account '%s' (%s).", selectedRoleName, selectedAccountName, selectedRegion)
	log.Printf("Session expires around: %s", expirationTime)
	log.Println("Run 'unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_REGION AWS_DEFAULT_REGION' or open a new terminal to clear.")
	return nil
}

// Function for Command Execution Mode (Corrected Env Handling)
func processAccountRegion(ctx context.Context, wg *sync.WaitGroup, baseCfg aws.Config, accountName, roleToAssume, commandToRun, region string, successCounter *atomic.Int64) {
	defer wg.Done()

	accountID, ok := accounts[accountName]
	if !ok {
		log.Printf("Error: Account ID not found for name '%s' (check config file '%s'). Skipping.", accountName, configFileName)
		return
	}

	finalCreds, err := assumeRole(ctx, baseCfg, accountID, roleToAssume, "CmdExecSession")
	if err != nil {
		log.Printf("ERROR: Assume Role Failed for Account: %s Region: %s Role: %s: %v", accountName, region, roleToAssume, err)
		return
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", commandToRun)

	// Prepare environment for the sub-command
	var cleanEnv []string
	originalEnv := os.Environ()
	for _, envVar := range originalEnv {
		if !strings.HasPrefix(envVar, "AWS_PROFILE=") &&
			!strings.HasPrefix(envVar, "AWS_ACCESS_KEY_ID=") &&
			!strings.HasPrefix(envVar, "AWS_SECRET_ACCESS_KEY=") &&
			!strings.HasPrefix(envVar, "AWS_SESSION_TOKEN=") &&
			!strings.HasPrefix(envVar, "AWS_REGION=") &&
			!strings.HasPrefix(envVar, "AWS_DEFAULT_REGION=") &&
			!strings.HasPrefix(envVar, "AWS_CONFIG_FILE=") &&
			!strings.HasPrefix(envVar, "AWS_SHARED_CREDENTIALS_FILE=") {
			cleanEnv = append(cleanEnv, envVar)
		}
	}
	// Add the temporary credentials and target region
	cmd.Env = cleanEnv
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", *finalCreds.AccessKeyId))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", *finalCreds.SecretAccessKey))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SESSION_TOKEN=%s", *finalCreds.SessionToken))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_REGION=%s", region))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_DEFAULT_REGION=%s", region))

	var outb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &outb

	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			log.Printf("ERROR executing command for Account: %s Region: %s: %v", accountName, region, err)
			exitCode = -1
		}
	}

	fmt.Printf("--- Command Output (Account: %s, Region: %s, Exit Code: %d, Duration: %s) ---\n",
		accountName, region, exitCode, duration.Round(time.Millisecond))
	fmt.Println(strings.TrimSpace(outb.String()))
	if exitCode == 0 {
		successCounter.Add(1)
	}
}

func main() {
	role := flag.String("r", "", "IAM role name (Command Mode only).")
	command := flag.String("c", "", "AWS CLI command to execute (Command Mode).")
	regionsStr := flag.String("regions", "", "Comma-separated regions (Command Mode).")
	processAll := flag.Bool("a", false, "Process ALL accounts from config (Command Mode).")
	selector := flag.String("s", "", "Process accounts matching selector (Command Mode).")
	envMode := flag.Bool("e", false, "Interactive mode for env setup.")
	configFile := flag.String("config", "", fmt.Sprintf("Path to %s file (optional).", configFileName))
	help := flag.Bool("h", false, "Display help message.")
	flag.Usage = usage
	flag.Parse()

	// Load Configuration
	configPath, err := findConfigPath(*configFile)
	if err != nil {
		log.Fatalf("Configuration Error: %v\nPlease ensure '%s' exists in ~/%s/ or ./, or use the -config flag.", err, configFileName, awsConfigDir)
	}
	loadedConfig, err := loadConfig(configPath)
	if err != nil {
		log.Fatalf("Configuration Error: %v", err)
	}
	accounts = loadedConfig.Accounts
	commonRegions = loadedConfig.CommonRegions
	roles = loadedConfig.Roles
	ctx := context.Background()

	if *help {
		usage()
		return
	}
	if *envMode {
		if flag.Lookup("r").Value.String() != "" {
			log.Printf("Error: The -r flag cannot be used with the environment setup mode (-e). Roles are selected interactively.")
			usage()
		}
		if *command != "" {
			log.Println("Warning: -c flag ignored in environment setup mode (-e).")
		}
		if *regionsStr != "" {
			log.Println("Warning: -regions flag ignored in environment setup mode (-e).")
		}
		if *processAll {
			log.Println("Warning: -a flag ignored in environment setup mode (-e).")
		}
		if *selector != "" {
			log.Println("Warning: -s flag ignored in environment setup mode (-e).")
		}
		err := setupEnvironmentInteractive(ctx)
		if err != nil {
			log.Fatalf("Error during interactive setup: %v", err)
		}
		os.Exit(0)
	} else {
		if *role == "" {
			log.Println("Error: Role name (-r) is mandatory in Command Execution Mode.")
			usage()
		}
		if *command == "" {
			log.Println("Error: AWS command (-c) is mandatory.")
			usage()
		}
		if *processAll && *selector != "" {
			log.Println("Error: Cannot use both -a and -s.")
			usage()
		}
		if !*processAll && *selector == "" {
			log.Println("Error: Must specify either -a or -s.")
			usage()
		}
		if _, err := exec.LookPath("aws"); err != nil {
			log.Fatalf("Error: AWS CLI command ('aws') not found. Please install it and ensure it's in your PATH for Command Execution Mode.")
		}

		// Determine Target Regions
		var targetRegions []string
		regionsInput := strings.TrimSpace(*regionsStr)
		if regionsInput != "" {
			targetRegions = strings.Split(regionsInput, ",")
			validRegions := make([]string, 0, len(targetRegions))
			for _, r := range targetRegions {
				trimmed := strings.TrimSpace(r)
				if trimmed != "" {
					validRegions = append(validRegions, trimmed)
				} else {
					log.Println("Warning: Empty region name ignored in -regions flag.")
				}
			}
			if len(validRegions) == 0 {
				log.Fatal("Error: No valid regions specified after parsing -regions flag.")
			}
			targetRegions = validRegions
		} else {
			log.Println("No regions specified via -regions flag. Determining default region...")
			tempCfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(baseProfileForAssume))
			if err != nil {
				log.Printf("Warning: Could not load AWS config to determine default region: %v. Falling back to '%s'.", err, fallbackRegion)
				targetRegions = []string{fallbackRegion}
			} else if tempCfg.Region == "" {
				log.Printf("Warning: Could not determine default region from AWS config/environment. Falling back to '%s'.", fallbackRegion)
				targetRegions = []string{fallbackRegion}
			} else {
				log.Printf("Using default region from AWS config/environment: %s", tempCfg.Region)
				targetRegions = []string{tempCfg.Region}
			}
		}

		// Determine Target Accounts
		var targetAccountNames []string
		allAccountNamesSorted := make([]string, 0, len(accounts))
		for name := range accounts {
			allAccountNamesSorted = append(allAccountNamesSorted, name)
		}
		sort.Strings(allAccountNamesSorted)
		if *processAll {
			targetAccountNames = allAccountNamesSorted
			log.Printf("Processing all %d accounts from config.", len(targetAccountNames))
		} else {
			selectorPatterns := strings.Fields(*selector)
			matchedAccounts := make(map[string]struct{})
			for _, accName := range allAccountNamesSorted {
				for _, pattern := range selectorPatterns {
					match, err := filepath.Match(pattern, accName)
					if err != nil {
						log.Printf("Warning: Invalid pattern '%s' in selector: %v. Skipping pattern.", pattern, err)
						continue
					}
					if match {
						if _, found := matchedAccounts[accName]; !found {
							targetAccountNames = append(targetAccountNames, accName)
							matchedAccounts[accName] = struct{}{}
						}
						break
					}
				}
			}
			sort.Strings(targetAccountNames)
			log.Printf("Selected %d accounts based on selector.", len(targetAccountNames))
		}
		if len(targetAccountNames) == 0 {
			if *selector != "" {
				log.Fatalf("Error: No accounts found in config matching the selector: \"%s\"", *selector)
			} else {
				log.Fatal("Error: No accounts selected for processing (and -a not used).")
			}
		}

		// Load Base AWS Configuration
		baseCfg, err := config.LoadDefaultConfig(ctx, config.WithSharedConfigProfile(baseProfileForAssume), config.WithRegion(fallbackRegion))
		if err != nil {
			log.Fatalf("Error loading base AWS configuration for profile '%s': %v", baseProfileForAssume, err)
		}

		// Main Processing Loop
		totalExecutions := len(targetAccountNames) * len(targetRegions)
		log.Printf("Targeting %d accounts across %d regions: %v", len(targetAccountNames), len(targetRegions), targetRegions)
		log.Printf("Total executions planned: %d", totalExecutions)
		for _, name := range targetAccountNames {
			log.Printf(" - Account: %s (%s)", name, accounts[name])
		}
		var wg sync.WaitGroup
		var successfulExecutions atomic.Int64
		for _, accountName := range targetAccountNames {
			for _, region := range targetRegions {
				wg.Add(1)
				go processAccountRegion(ctx, &wg, baseCfg, accountName, *role, *command, region, &successfulExecutions)
			}
		}
		wg.Wait()

		// Final Summary
		finalSuccessCount := successfulExecutions.Load()
		if finalSuccessCount == int64(totalExecutions) {
			log.Printf("All %d targeted executions (account/region pairs) processed successfully.", finalSuccessCount)
			os.Exit(0)
		} else {
			log.Printf("%d out of %d targeted executions (account/region pairs) processed successfully. One or more failed. Please review logs above.", finalSuccessCount, totalExecutions)
			os.Exit(1)
		}
	}
}

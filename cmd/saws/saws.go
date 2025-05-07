package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"saws/internal/app/saws"
	"saws/internal/pkg"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: saws <mode> [options]

Modes:
  -c <cmd>      Command Execution: Run <cmd> across accounts/regions.
                  Requires: -r, (-a | -s)
                  Optional: -regions
  -e            Interactive Sub-Shell: Start a sub-shell with assumed role credentials.
                  Optional: -s, -r, -region (or use env vars / interactive prompts)
  -ssm          SSM Session: Start an interactive SSM session to an EC2 instance.
                  Optional: -i, -s, -r, -region (prompts if needed)
  -ecs          ECS Exec Session: Start an interactive exec session to an ECS container.
                  Optional: --ecs-cluster, --ecs-task, --ecs-container, --ecs-command,
                            -s, -r, -region (prompts if needed)

Common Options:
  -r <role>     IAM role name to assume.
  -s <selector> Account selector (Cmd Mode: comma-sep names/wildcards; Others: single name/wildcard).
  -region <reg> AWS region (for -e, -ssm, -ecs modes).
  -config <path> Path to saws-config.yaml file.
  -v            Enable verbose logging.
  -h            Display this help message.

Command Mode Options (-c):
  -regions <regs> Comma-separated regions for command execution.
  -a             Process all accounts defined in config.

SSM Session Mode Options (-ssm):
  -i <inst-id>  Target EC2 instance ID (if omitted, instances will be listed for selection).

ECS Exec Session Mode Options (-ecs):
  --ecs-cluster <name|arn>  Target ECS cluster.
  --ecs-task <id|arn>       Target ECS task.
  --ecs-container <name>    Target container name within the task.
  --ecs-command <cmd>       Command to execute in container (default: /bin/sh).

Examples:
  # Command Execution: Run 'aws s3 ls' in eu-west-1 for prod-* accounts as 'ReadOnly'
  saws -c "aws s3 ls" -r ReadOnly -s "prod-*,dev-account" -regions "eu-west-1,us-east-1"

  # Interactive Sub-Shell: Start shell
  saws -e
  saws -e -s dev-1 -r Admin -region us-east-1

  # SSM Session (direct connect):
  saws -ssm
  saws -ssm -i i-0123... -s prod-web -r Admin -region eu-central-1
  saws -ssm -s prod-db -r DBAccess -region us-west-2

  # ECS Exec Session (direct connect to a specific container):
  saws -ecs --ecs-cluster my-cluster --ecs-task a1b2c3d4e5 --ecs-container my-app-container -s prod-app -r AppAdmin -region us-east-1

  # ECS Exec Session (interactive selection):
  saws -ecs -s dev-app -r Developer -region eu-west-1
`)
	os.Exit(1)
}

func main() {
	log.SetFlags(log.Ltime)

	// Common flags
	roleCmd := flag.String("r", "", "IAM role name.")
	selector := flag.String("s", "", "Account name selector(s).")
	configFile := flag.String("config", "", fmt.Sprintf("Path to SAWS %s file.", pkg.ConfigFileName))
	help := flag.Bool("h", false, "Display help message.")
	contextRegionFlag := flag.String("region", "", "AWS region (for -e, -ssm, or -ecs modes).")
	verbose := flag.Bool("v", false, "Enable verbose logging.")

	// Command Mode flags
	command := flag.String("c", "", "Command to execute (enables Command Execution Mode).")
	cmdRegionsStr := flag.String("regions", "", "Comma-separated regions for command execution (Command Mode only).")
	processAll := flag.Bool("a", false, "Process ALL accounts (Command Mode only).")

	// Interactive Sub-Shell Mode flag
	sessionModeFlag := flag.Bool("e", false, "Enable interactive sub-shell session mode.")

	// SSM Session Mode flags
	ssmSessionFlag := flag.Bool("ssm", false, "Enable interactive SSM session to an EC2 instance.")
	instanceIDFlag := flag.String("i", "", "Target EC2 instance ID for SSM session (Optional).")

	// ECS Exec Session Mode flags
	ecsModeFlag := flag.Bool("ecs", false, "Enable interactive ECS exec session mode.")
	ecsClusterFlag := flag.String("ecs-cluster", "", "Target ECS cluster name or ARN (ECS Mode only).")
	ecsTaskFlag := flag.String("ecs-task", "", "Target ECS task ID or ARN (ECS Mode only).")
	ecsContainerFlag := flag.String("ecs-container", "", "Target ECS container name (ECS Mode only).")
	ecsCommandFlag := flag.String("ecs-command", "", "Command to run in the ECS container (default: /bin/sh) (ECS Mode only).")

	flag.Usage = usage
	flag.Parse()

	pkg.VerboseMode = *verbose

	if !pkg.VerboseMode {
		log.SetOutput(io.Discard)
	} else {
		log.SetOutput(os.Stderr)
	}

	sawsConfigPath, err := pkg.FindConfigPath(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SAWS Config Error: %v\n", err)
		os.Exit(1)
	}
	appConfig, err := pkg.LoadConfig(sawsConfigPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "SAWS Config Error: %v\n", err)
		os.Exit(1)
	}
	ctx := context.Background()

	if *help {
		usage()
		return
	}

	isCommandMode := *command != ""
	isSessionMode := *sessionModeFlag
	isSSMSessionMode := *ssmSessionFlag
	isECSMode := *ecsModeFlag

	modeCount := 0
	if isCommandMode {
		modeCount++
	}
	if isSessionMode {
		modeCount++
	}
	if isSSMSessionMode {
		modeCount++
	}
	if isECSMode {
		modeCount++
	}

	if modeCount > 1 {
		fmt.Fprintln(os.Stderr, "Error: Cannot use -c, -e, -ssm, and -ecs flags together. Please choose one mode.")
		usage()
	}
	if modeCount == 0 {
		fmt.Fprintln(os.Stderr, "Error: No mode selected. Please specify -c, -e, -ssm, or -ecs.")
		usage()
	}

	if isSessionMode {
		if *cmdRegionsStr != "" {
			fmt.Fprintln(os.Stderr, "Warning: -regions flag ignored in interactive session mode (-e). Use -region for context.")
		}
		if *processAll {
			fmt.Fprintln(os.Stderr, "Warning: -a flag ignored in interactive session mode (-e).")
		}
		if *instanceIDFlag != "" {
			fmt.Fprintln(os.Stderr, "Warning: -i (instance-id) flag ignored in interactive sub-shell mode (-e). Used with -ssm.")
		}
		// Warnings for ECS flags if -e is used
		if *ecsClusterFlag != "" || *ecsTaskFlag != "" || *ecsContainerFlag != "" || *ecsCommandFlag != "" {
			fmt.Fprintln(os.Stderr, "Warning: --ecs-* flags are ignored in interactive sub-shell mode (-e). Used with -ecs.")
		}

		sCtx, creds, errCtx := pkg.EstablishAWSContextAndAssumeRole(ctx, *selector, *roleCmd, *contextRegionFlag, "InteractiveSubShell")
		if errCtx != nil {
			fmt.Fprintf(os.Stderr, "Failed to establish AWS context for sub-shell: %v\n", errCtx)
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "# Optional: To show saws context in your prompt (for -e sub-shell), add to your ~/.bashrc or ~/.zshrc:")
		fmt.Fprintln(os.Stderr, "#   if [ -n \"$SAWS_INFO_ACCOUNT_NAME\" ]; then")
		fmt.Fprintln(os.Stderr, "#     SAWS_PROMPT=\"(\\[\\033[01;32m\\]${SAWS_INFO_ACCOUNT_NAME}(${SAWS_INFO_ACCOUNT_ID})/${SAWS_INFO_ROLE_NAME}/${SAWS_INFO_REGION}\\[\\033[00m\\]):\\[\\033[01;34m\\]\\w\\[\\033[00m\\]\\$ \"")
		fmt.Fprintln(os.Stderr, "#     PS1=\"$SAWS_PROMPT\" # Or integrate into your existing PS1 logic")
		fmt.Fprintln(os.Stderr, "#   fi")
		fmt.Fprintln(os.Stderr, "# -------------------------------------------------------------------------------------------------")

		errCtx = saws.StartInteractiveSubShell(sCtx, creds)
		if errCtx != nil {
			fmt.Fprintf(os.Stderr, "Interactive sub-shell session failed: %v\n", errCtx)
			os.Exit(1)
		}
		os.Exit(0)

	} else if isSSMSessionMode {
		if *cmdRegionsStr != "" {
			fmt.Fprintln(os.Stderr, "Warning: -regions flag ignored in SSM session mode (-ssm). Use -region for context.")
		}
		if *processAll {
			fmt.Fprintln(os.Stderr, "Warning: -a flag ignored in SSM session mode (-ssm).")
		}
		if *command != "" { // -c flag for command mode
			fmt.Fprintln(os.Stderr, "Warning: -c (command) flag ignored in SSM session mode (-ssm).")
		}
		// Warnings for ECS flags if -ssm is used
		if *ecsClusterFlag != "" || *ecsTaskFlag != "" || *ecsContainerFlag != "" || *ecsCommandFlag != "" {
			fmt.Fprintln(os.Stderr, "Warning: --ecs-* flags are ignored in SSM session mode (-ssm). Used with -ecs.")
		}

		errCtx := saws.HandleSSMSession(ctx, *instanceIDFlag, *selector, *roleCmd, *contextRegionFlag)
		if errCtx != nil {
			fmt.Fprintf(os.Stderr, "SSM session failed: %v\n", errCtx)
			os.Exit(1)
		}
		os.Exit(0)

	} else if isECSMode {
		if *cmdRegionsStr != "" {
			fmt.Fprintln(os.Stderr, "Warning: -regions flag ignored in ECS exec session mode (-ecs). Use -region for context.")
		}
		if *processAll {
			fmt.Fprintln(os.Stderr, "Warning: -a flag ignored in ECS exec session mode (-ecs).")
		}
		if *command != "" { // -c flag for command execution mode
			fmt.Fprintln(os.Stderr, "Warning: -c (command execution mode command) flag ignored in ECS exec session mode (-ecs). Use --ecs-command for container command.")
		}
		if *instanceIDFlag != "" { // -i flag for ssm mode
			fmt.Fprintln(os.Stderr, "Warning: -i (instance-id) flag ignored in ECS exec session mode (-ecs).")
		}

		errCtx := saws.HandleEcsExecSession(ctx, appConfig, *ecsClusterFlag, *ecsTaskFlag, *ecsContainerFlag, *ecsCommandFlag, *selector, *roleCmd, *contextRegionFlag)
		if errCtx != nil {
			fmt.Fprintf(os.Stderr, "ECS exec session failed: %v\n", errCtx)
			os.Exit(1)
		}
		os.Exit(0)

	} else if isCommandMode {
		if *roleCmd == "" {
			fmt.Fprintln(os.Stderr, "Error: Role (-r) is mandatory for Command Execution Mode.")
			usage()
		}
		if *processAll && *selector != "" {
			fmt.Fprintln(os.Stderr, "Error: Cannot use both -a and -s in Command Mode.")
			usage()
		}
		if !*processAll && *selector == "" {
			fmt.Fprintln(os.Stderr, "Error: Must use -a or -s in Command Mode.")
			usage()
		}
		if _, errLook := exec.LookPath("aws"); errLook != nil {
			fmt.Fprintf(os.Stderr, "Error: AWS CLI ('aws') not found in PATH. Required for Command Mode.\n")
			os.Exit(1)
		}
		// Warnings for ECS flags if -c is used
		if *ecsClusterFlag != "" || *ecsTaskFlag != "" || *ecsContainerFlag != "" || *ecsCommandFlag != "" {
			fmt.Fprintln(os.Stderr, "Warning: --ecs-* flags are ignored in command execution mode (-c). Used with -ecs.")
		}
		if *instanceIDFlag != "" {
			fmt.Fprintln(os.Stderr, "Warning: -i (instance-id) flag ignored in command execution mode (-c). Used with -ssm.")
		}

		var targetRegionsCmd []string
		regionsInput := strings.TrimSpace(*cmdRegionsStr)
		if regionsInput != "" {
			rawRegions := strings.Split(regionsInput, ",")
			for _, r := range rawRegions {
				trimmed := strings.TrimSpace(r)
				if trimmed != "" {
					targetRegionsCmd = append(targetRegionsCmd, trimmed)
				}
			}
			if len(targetRegionsCmd) == 0 {
				fmt.Fprintln(os.Stderr, "Error: -regions flag provided but contained no valid region names after trimming.")
				os.Exit(1)
			}
			pkg.LogVerbosef("Cmd Mode: Using specified regions: %v", targetRegionsCmd)
		} else {
			pkg.LogVerbosef("Cmd Mode: No -regions flag provided. Determining default region...")
			tempCfg, errCfg := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(pkg.BaseProfileForAssume))
			defaultCmdRegion := pkg.FallbackRegion
			if errCfg != nil {
				pkg.LogVerbosef("Warning: Could not load AWS config to determine default region: %v. Falling back to '%s'.", errCfg, defaultCmdRegion)
			} else if tempCfg.Region == "" {
				pkg.LogVerbosef("Warning: Could not determine default region from AWS config/environment. Falling back to '%s'.", defaultCmdRegion)
			} else {
				defaultCmdRegion = tempCfg.Region
				pkg.LogVerbosef("Cmd Mode: Using default region from AWS config/environment: %s", defaultCmdRegion)
			}
			targetRegionsCmd = []string{defaultCmdRegion}
		}

		var targetAccountNames []string
		allAccountNamesSorted := make([]string, 0, len(appConfig.Accounts))
		for name := range appConfig.Accounts {
			allAccountNamesSorted = append(allAccountNamesSorted, name)
		}
		sort.Strings(allAccountNamesSorted)
		if *processAll {
			targetAccountNames = allAccountNamesSorted
			pkg.LogVerbosef("Cmd Mode Accounts: Processing all %d defined accounts.", len(targetAccountNames))
		} else {
			rawPatterns := strings.Split(*selector, ",")
			selectorPatterns := []string{}
			for _, p := range rawPatterns {
				trimmed := strings.TrimSpace(p)
				if trimmed != "" {
					selectorPatterns = append(selectorPatterns, trimmed)
				}
			}
			if len(selectorPatterns) == 0 {
				fmt.Fprintf(os.Stderr, "Error: Selector flag '-s \"%s\"' provided no valid names/patterns.\n", *selector)
				os.Exit(1)
			}
			matchedAccountsMap := make(map[string]struct{})
			pkg.LogVerbosef("Cmd Mode: Applying selector patterns: %v", selectorPatterns)
			for _, accName := range allAccountNamesSorted {
				for _, pattern := range selectorPatterns {
					match, errMatch := filepath.Match(pattern, accName)
					if errMatch != nil {
						pkg.LogVerbosef("Warning: Invalid pattern '%s' in selector: %v.", pattern, errMatch)
						continue
					}
					if match {
						matchedAccountsMap[accName] = struct{}{}
						break
					}
				}
			}
			for accName := range matchedAccountsMap {
				targetAccountNames = append(targetAccountNames, accName)
			}
			sort.Strings(targetAccountNames)
			pkg.LogVerbosef("Cmd Mode: Selected %d account(s) using selector '%s': %v", len(targetAccountNames), *selector, targetAccountNames)
			if len(targetAccountNames) == 0 {
				fmt.Fprintf(os.Stderr, "Error: No accounts found matching selector patterns: %v\n", selectorPatterns)
				os.Exit(1)
			}
		}

		baseCfgAWS, errCfg := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(pkg.BaseProfileForAssume), awsconfig.WithRegion(pkg.FallbackRegion))
		if errCfg != nil {
			fmt.Fprintf(os.Stderr, "Error loading base AWS configuration (profile '%s'): %v\n", pkg.BaseProfileForAssume, errCfg)
			os.Exit(1)
		}

		totalExecutions := len(targetAccountNames) * len(targetRegionsCmd)
		pkg.LogVerbosef("Cmd Mode: Planning %d executions (%d accounts x %d regions).", totalExecutions, len(targetAccountNames), len(targetRegionsCmd))
		var wg sync.WaitGroup
		var successfulExecutions atomic.Int64
		startTime := time.Now()

		for _, accountName := range targetAccountNames {
			for _, region := range targetRegionsCmd {
				wg.Add(1)
				accName := accountName
				reg := region
				go saws.ProcessAccountRegion(ctx, &wg, baseCfgAWS, appConfig, accName, *roleCmd, *command, reg, &successfulExecutions)
			}
		}
		wg.Wait()
		totalDuration := time.Since(startTime)

		finalSuccessCount := successfulExecutions.Load()
		pkg.LogVerbosef("Cmd Mode: Finished %d executions in %s.", totalExecutions, totalDuration.Round(time.Second))
		if finalSuccessCount == int64(totalExecutions) {
			pkg.LogVerbosef("Cmd Mode: All %d executions completed successfully.", finalSuccessCount)
			os.Exit(0)
		} else {
			fmt.Fprintf(os.Stderr, "Cmd Mode: %d out of %d targeted executions completed successfully. %d failed.\n", finalSuccessCount, totalExecutions, int64(totalExecutions)-finalSuccessCount)
			os.Exit(1)
		}
	}
}

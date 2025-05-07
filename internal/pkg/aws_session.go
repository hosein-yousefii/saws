package pkg

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/AlecAivazis/survey/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

type SelectedContext struct {
	AccountName string
	AccountID   string
	RoleName    string
	Region      string
}

const (
	BaseProfileForAssume   = "default"
	FallbackRegion         = "eu-west-1"
	SessionDurationSeconds = 3600
)

func AssumeRole(ctx context.Context, baseCfg aws.Config, accountID, roleToAssume, sessionNameSuffix string) (*ststypes.Credentials, error) {
	if baseCfg.Region == "" {
		LogVerbosef("Warning: base AWS config for STS AssumeRole call had no region, defaulting to %s", FallbackRegion)
		baseCfg.Region = FallbackRegion
	}

	stsClient := sts.NewFromConfig(baseCfg)
	roleArn := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleToAssume)

	safeRolePart := strings.ReplaceAll(roleToAssume, "/", "-")
	safeRolePart = strings.ReplaceAll(safeRolePart, " ", "_")
	if len(safeRolePart) > 30 {
		safeRolePart = safeRolePart[:30]
	}

	sessionName := fmt.Sprintf("%s-%s-%d", sessionNameSuffix, safeRolePart, os.Getpid())
	if len(sessionName) > 64 {
		sessionName = sessionName[:64]
	}

	AssumeRoleInput := &sts.AssumeRoleInput{
		RoleArn:         aws.String(roleArn),
		RoleSessionName: aws.String(sessionName),
		DurationSeconds: aws.Int32(SessionDurationSeconds),
	}
	LogVerbosef("Attempting AssumeRole: ARN=%s, SessionName=%s", roleArn, sessionName)

	AssumeRoleOutput, err := stsClient.AssumeRole(ctx, AssumeRoleInput)
	if err != nil {
		return nil, fmt.Errorf("sts:AssumeRole call failed for role ARN %s: %w", roleArn, err)
	}

	if AssumeRoleOutput.Credentials == nil ||
		AssumeRoleOutput.Credentials.AccessKeyId == nil ||
		AssumeRoleOutput.Credentials.SecretAccessKey == nil ||
		AssumeRoleOutput.Credentials.SessionToken == nil {
		return nil, fmt.Errorf("assume role response for role ARN %s did not contain valid credentials", roleArn)
	}

	LogVerbosef("Successfully assumed role %s", roleArn)
	return AssumeRoleOutput.Credentials, nil
}

func EstablishAWSContextAndAssumeRole(ctx context.Context, accountSelectorFlag, roleFlag, regionFlagFromCmd string, sessionType string) (*SelectedContext, *ststypes.Credentials, error) {
	if len(accounts) == 0 {
		return nil, nil, errors.New("internal error: accounts map is empty (SAWS config not loaded or no accounts defined)")
	}

	sCtx := &SelectedContext{}

	allAccountNames := make([]string, 0, len(accounts))
	for name := range accounts {
		allAccountNames = append(allAccountNames, name)
	}
	sort.Strings(allAccountNames)

	selectedAccountName := ""
	currentAccountSelector := accountSelectorFlag
	if currentAccountSelector == "" {
		currentAccountSelector = os.Getenv(envAccountVar)
		if currentAccountSelector != "" {
			LogVerbosef("Using account selector '%s' from %s environment variable.", currentAccountSelector, envAccountVar)
		}
	} else {
		LogVerbosef("Using account selector '%s' from -s flag.", currentAccountSelector)
	}

	if currentAccountSelector != "" {
		matchedAccountNames := []string{}
		for _, accName := range allAccountNames {
			if currentAccountSelector == accName {
				matchedAccountNames = []string{accName}
				break
			}
			match, err := filepath.Match(currentAccountSelector, accName)
			if err != nil {
				LogVerbosef("Warning: Invalid pattern '%s' in selector: %v. Skipping this pattern for account '%s'.", currentAccountSelector, err, accName)
				continue
			}
			if match {
				matchedAccountNames = append(matchedAccountNames, accName)
			}
		}
		if len(matchedAccountNames) == 1 {
			selectedAccountName = matchedAccountNames[0]
			LogVerbosef("Automatically selected account '%s' based on unique selector match '%s'", selectedAccountName, currentAccountSelector)
		} else if len(matchedAccountNames) > 1 {
			LogVerbosef("Selector '%s' matched multiple accounts. Please choose one:", currentAccountSelector)
			displayOptions := make([]string, len(matchedAccountNames))
			optionToAccountNameMap := make(map[string]string)
			sort.Strings(matchedAccountNames)
			for i, name := range matchedAccountNames {
				displayStr := fmt.Sprintf("%s (%s)", name, accounts[name])
				displayOptions[i] = displayStr
				optionToAccountNameMap[displayStr] = name
			}
			chosenDisplayStr := ""
			promptAccount := &survey.Select{Message: "Choose an AWS Account:", Options: displayOptions, PageSize: 15}
			err := survey.AskOne(promptAccount, &chosenDisplayStr, survey.WithValidator(survey.Required))
			if err != nil {
				return nil, nil, fmt.Errorf("account selection from multiple matches failed: %w", err)
			}
			selectedAccountName = optionToAccountNameMap[chosenDisplayStr]
		} else {
			return nil, nil, fmt.Errorf("selector '%s' (from flag or %s) did not match any accounts in SAWS config", currentAccountSelector, envAccountVar)
		}
	}

	if selectedAccountName == "" {
		fmt.Fprintln(os.Stderr, "Please select an account:")
		displayOptions := make([]string, len(allAccountNames))
		optionToAccountNameMap := make(map[string]string)
		for i, name := range allAccountNames {
			displayStr := fmt.Sprintf("%s (%s)", name, accounts[name])
			displayOptions[i] = displayStr
			optionToAccountNameMap[displayStr] = name
		}
		chosenDisplayStr := ""
		promptAccount := &survey.Select{Message: "Choose an AWS Account:", Options: displayOptions, PageSize: 15}
		err := survey.AskOne(promptAccount, &chosenDisplayStr, survey.WithValidator(survey.Required))
		if err != nil {
			return nil, nil, fmt.Errorf("interactive account selection failed: %w", err)
		}
		selectedAccountName = optionToAccountNameMap[chosenDisplayStr]
	}
	sCtx.AccountName = selectedAccountName
	sCtx.AccountID = accounts[selectedAccountName]

	selectedRoleName := ""
	currentRoleName := roleFlag
	if currentRoleName == "" {
		currentRoleName = os.Getenv(envRoleVar)
		if currentRoleName != "" {
			LogVerbosef("Using role '%s' from %s environment variable.", currentRoleName, envRoleVar)
		}
	} else {
		LogVerbosef("Using role '%s' from -r flag.", currentRoleName)
	}

	if currentRoleName != "" {
		selectedRoleName = currentRoleName
		if friendlyRole, ok := roles[currentRoleName]; ok {
			LogVerbosef("Interpreted non-interactive role '%s' as friendly name for actual role '%s'.", currentRoleName, friendlyRole)
			selectedRoleName = friendlyRole
		}
	} else {
		if len(roles) > 0 {
			fmt.Fprintln(os.Stderr, "Please select a role:")
			friendlyRoleNames := make([]string, 0, len(roles))
			for friendlyName := range roles {
				friendlyRoleNames = append(friendlyRoleNames, friendlyName)
			}
			sort.Strings(friendlyRoleNames)
			chosenFriendlyName := ""
			promptRoleSelect := &survey.Select{Message: "Choose Role to Assume:", Options: friendlyRoleNames, PageSize: 15}
			err := survey.AskOne(promptRoleSelect, &chosenFriendlyName, survey.WithValidator(survey.Required))
			if err != nil {
				return nil, nil, fmt.Errorf("interactive role selection failed: %w", err)
			}
			selectedRoleName = roles[chosenFriendlyName]
			LogVerbosef("Selected friendly role '%s' -> actual role '%s'.", chosenFriendlyName, selectedRoleName)
		} else {
			fmt.Fprintln(os.Stderr, "No 'roles' section in config. Please provide role name:")
			promptManualRole := &survey.Input{Message: "Enter the exact IAM Role Name to Assume:"}
			err := survey.AskOne(promptManualRole, &selectedRoleName, survey.WithValidator(survey.Required))
			if err != nil {
				return nil, nil, fmt.Errorf("manual role input failed: %w", err)
			}
		}
	}
	if selectedRoleName == "" {
		return nil, nil, errors.New("could not determine role to assume")
	}
	sCtx.RoleName = selectedRoleName

	selectedRegion := ""
	currentRegion := regionFlagFromCmd
	if currentRegion == "" {
		currentRegion = os.Getenv(envRegionVar)
		if currentRegion != "" {
			LogVerbosef("Using region '%s' from %s environment variable.", currentRegion, envRegionVar)
		}
	} else {
		LogVerbosef("Using region '%s' from -region flag.", currentRegion)
	}

	if currentRegion != "" {
		selectedRegion = currentRegion
	} else {
		availablePromptRegions := commonRegions
		if len(availablePromptRegions) == 0 {
			LogVerbosef("No 'common_regions' defined in SAWS config. Trying to detect default AWS region from your environment...")
			tempCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(BaseProfileForAssume))
			if err == nil && tempCfg.Region != "" {
				LogVerbosef("Detected default AWS region: %s. Using it as the only option for selection.", tempCfg.Region)
				availablePromptRegions = []string{tempCfg.Region}
			} else {
				LogVerbosef("Could not detect default AWS region from environment. Please provide the region manually.")
			}
		}
		if len(availablePromptRegions) > 0 {
			defaultRegionChoice := FallbackRegion
			tempCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(BaseProfileForAssume))
			if err == nil && tempCfg.Region != "" {
				defaultRegionChoice = tempCfg.Region
			}
			foundDefaultInList := false
			for _, r := range availablePromptRegions {
				if r == defaultRegionChoice {
					foundDefaultInList = true
					break
				}
			}
			if !foundDefaultInList && len(availablePromptRegions) > 0 {
				defaultRegionChoice = availablePromptRegions[0]
			}
			fmt.Fprintln(os.Stderr, "Please select a region:")
			promptRegion := &survey.Select{Message: "Choose AWS Region:", Options: availablePromptRegions, Default: defaultRegionChoice, PageSize: 10}
			err = survey.AskOne(promptRegion, &selectedRegion, survey.WithValidator(survey.Required))
			if err != nil {
				return nil, nil, fmt.Errorf("interactive region selection failed: %w", err)
			}
		} else {
			fmt.Fprintln(os.Stderr, "Please provide region manually:")
			promptManualRegion := &survey.Input{Message: "Enter the AWS Region:"}
			err := survey.AskOne(promptManualRegion, &selectedRegion, survey.WithValidator(survey.Required))
			if err != nil {
				return nil, nil, fmt.Errorf("manual region input failed: %w", err)
			}
		}
	}
	if selectedRegion == "" {
		return nil, nil, errors.New("could not determine region")
	}
	sCtx.Region = selectedRegion

	LogVerbosef("Context established: Account=%s(%s), Role=%s, Region=%s. Assuming role for session type: %s", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName, sCtx.Region, sessionType)
	baseCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(BaseProfileForAssume), awsconfig.WithRegion(FallbackRegion))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load base AWS configuration for STS AssumeRole call: %w", err)
	}
	finalCreds, err := AssumeRole(ctx, baseCfg, sCtx.AccountID, sCtx.RoleName, sessionType)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to assume role '%s' in account %s (%s) for region %s: %w", sCtx.RoleName, sCtx.AccountName, sCtx.AccountID, sCtx.Region, err)
	}

	return sCtx, finalCreds, nil
}

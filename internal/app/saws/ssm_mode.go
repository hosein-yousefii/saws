package saws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"saws/internal/pkg"

	"github.com/AlecAivazis/survey/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func GetSSMInstanceInfoList(ctx context.Context, credsaws aws.Credentials, region string) ([]ssmtypes.InstanceInformation, error) {
	awsSDKConfig, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) {
			return credsaws, nil
		})),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS SDK config for SSM client: %w", err)
	}
	ssmClient := ssm.NewFromConfig(awsSDKConfig)

	var allInstanceInfo []ssmtypes.InstanceInformation
	var nextToken *string
	maxResultsPerPage := int32(50)

	pkg.LogVerbosef("Fetching SSM instance information from region %s...", region)
	pageCount := 0
	for {
		pageCount++
		input := &ssm.DescribeInstanceInformationInput{MaxResults: &maxResultsPerPage, NextToken: nextToken}
		resp, err := ssmClient.DescribeInstanceInformation(ctx, input)
		if err != nil {
			return nil, fmt.Errorf("failed to describe SSM instance information (page %d): %w", pageCount, err)
		}
		if len(resp.InstanceInformationList) > 0 {
			pkg.LogVerbosef("Fetched page %d with %d instances.", pageCount, len(resp.InstanceInformationList))
		}
		allInstanceInfo = append(allInstanceInfo, resp.InstanceInformationList...)
		if resp.NextToken == nil {
			break
		}
		nextToken = resp.NextToken
	}
	pkg.LogVerbosef("Finished fetching SSM instances. Total found: %d", len(allInstanceInfo))
	return allInstanceInfo, nil
}

func HandleSSMSession(ctx context.Context, instanceIDFromFlag, accountSelectorFlag, roleFlag, regionFlagFromCmd string) error {
	pkg.LogVerbosef("Preparing for SSM session...")
	sCtx, creds, err := pkg.EstablishAWSContextAndAssumeRole(ctx, accountSelectorFlag, roleFlag, regionFlagFromCmd, "SSMSessionSetup")
	if err != nil {
		return fmt.Errorf("could not establish AWS context for SSM session: %w", err)
	}

	targetInstanceID := instanceIDFromFlag
	awsCreds := aws.Credentials{AccessKeyID: *creds.AccessKeyId, SecretAccessKey: *creds.SecretAccessKey, SessionToken: *creds.SessionToken, Source: "SawsAssumedRoleForSSM"}

	if targetInstanceID == "" {
		pkg.LogVerbosef("No instance ID provided via -i flag. Listing available SSM-managed instances for selection...")
		instanceList, errList := GetSSMInstanceInfoList(ctx, awsCreds, sCtx.Region)
		if errList != nil {
			return fmt.Errorf("failed to list SSM instances for selection: %w", errList)
		}
		if len(instanceList) == 0 {
			fmt.Fprintf(os.Stderr, "No SSM-managed instances found in Account: %s (%s), Region: %s to select from.\n", sCtx.AccountName, sCtx.AccountID, sCtx.Region)
			return nil // Not an error, just nothing to do
		}

		instanceOptions := make([]string, len(instanceList))
		optionToInstanceID := make(map[string]string)
		sort.SliceStable(instanceList, func(i, j int) bool {
			nameI := ""
			if instanceList[i].ComputerName != nil {
				nameI = *instanceList[i].ComputerName
			}
			nameJ := ""
			if instanceList[j].ComputerName != nil {
				nameJ = *instanceList[j].ComputerName
			}
			if nameI != nameJ {
				return nameI < nameJ
			}
			idI := ""
			if instanceList[i].InstanceId != nil {
				idI = *instanceList[i].InstanceId
			}
			idJ := ""
			if instanceList[j].InstanceId != nil {
				idJ = *instanceList[j].InstanceId
			}
			return idI < idJ
		})

		for i, info := range instanceList {
			instID := "N/A"
			if info.InstanceId != nil {
				instID = *info.InstanceId
			}
			compName := "N/A"
			if info.ComputerName != nil {
				compName = *info.ComputerName
			}
			platType := "N/A"
			if info.PlatformType != "" {
				platType = string(info.PlatformType)
			}
			ipAddr := "N/A"
			if info.IPAddress != nil {
				ipAddr = *info.IPAddress
			}
			pingStat := "N/A"
			if info.PingStatus != "" {
				pingStat = string(info.PingStatus)
			}

			displayStr := fmt.Sprintf("%-19s | %-20s | %-7s | %-15s | %s", instID, compName, platType, ipAddr, pingStat)
			instanceOptions[i] = displayStr
			optionToInstanceID[displayStr] = instID
		}

		chosenDisplayStr := ""
		prompt := &survey.Select{Message: "Choose an SSM instance to connect to:", Options: instanceOptions, PageSize: 15}
		errSurvey := survey.AskOne(prompt, &chosenDisplayStr, survey.WithValidator(survey.Required))
		if errSurvey != nil {
			return fmt.Errorf("instance selection failed: %w", errSurvey)
		}
		targetInstanceID = optionToInstanceID[chosenDisplayStr]
		pkg.LogVerbosef("Instance '%s' selected for SSM session.", targetInstanceID)
	} else {
		pkg.LogVerbosef("Instance ID '%s' provided via -i flag. Attempting direct connection.", targetInstanceID)
	}

	if targetInstanceID == "" {
		return errors.New("internal error: target instance ID for SSM session is empty after selection/flag check")
	}

	awsCLIPath, err := exec.LookPath("aws")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: AWS CLI ('aws') not found in PATH. Required for SSM Session Mode.")
		fmt.Fprintln(os.Stderr, "Please install AWS CLI and Session Manager plugin.")
		return errors.New("aws cli not found")
	}
	pkg.LogVerbosef("Using AWS CLI at: %s", awsCLIPath)

	pkg.LogVerbosef("Preparing environment for SSM session command...")
	currentEnv := os.Environ()
	newEnv := []string{}
	for _, e := range currentEnv {
		if !strings.HasPrefix(e, "AWS_ACCESS_KEY_ID=") && !strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") && !strings.HasPrefix(e, "AWS_SESSION_TOKEN=") && !strings.HasPrefix(e, "AWS_SECURITY_TOKEN=") && !strings.HasPrefix(e, "AWS_REGION=") && !strings.HasPrefix(e, "AWS_DEFAULT_REGION=") && !strings.HasPrefix(e, "AWS_PROFILE=") {
			newEnv = append(newEnv, e)
		}
	}
	newEnv = append(newEnv, fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", *creds.AccessKeyId))
	newEnv = append(newEnv, fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", *creds.SecretAccessKey))
	newEnv = append(newEnv, fmt.Sprintf("AWS_SESSION_TOKEN=%s", *creds.SessionToken))
	newEnv = append(newEnv, fmt.Sprintf("AWS_REGION=%s", sCtx.Region))
	newEnv = append(newEnv, fmt.Sprintf("AWS_DEFAULT_REGION=%s", sCtx.Region))

	fmt.Fprintf(os.Stderr, "Starting SSM session to instance '%s' in region '%s'...\n", targetInstanceID, sCtx.Region)
	if creds.Expiration != nil {
		fmt.Fprintf(os.Stderr, "Context: Account=%s(%s), Role=%s. Session expires around: %s\n", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName, creds.Expiration.Local().Format(time.RFC1123))
	} else {
		fmt.Fprintf(os.Stderr, "Context: Account=%s(%s), Role=%s. Session expiration time not available.\n", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName)
	}
	fmt.Fprintln(os.Stderr, "Ensure the Session Manager plugin for AWS CLI is installed. Type 'exit' or Ctrl+D to end session.")

	ssmCmd := exec.Command(awsCLIPath, "ssm", "start-session", "--target", targetInstanceID, "--region", sCtx.Region)
	ssmCmd.Env = newEnv
	ssmCmd.Stdin = os.Stdin
	ssmCmd.Stdout = os.Stdout
	ssmCmd.Stderr = os.Stderr

	// Set up a channel to handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGQUIT)

	go func() {
		for sig := range sigChan {
			if ssmCmd.Process != nil {
				pkg.LogVerbosef("Forwarding signal: %v to SSM process", sig)
				ssmCmd.Process.Signal(sig)
			}
		}
	}()

	err = ssmCmd.Run()
	signal.Stop(sigChan)
	close(sigChan)

	pkg.LogVerbosef("SSM session ended.")
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			pkg.LogVerbosef("SSM command exited with status: %s.", exitErr.Error())
		} else {
			return fmt.Errorf("failed to run 'aws ssm start-session': %w", err)
		}
	}
	return nil
}

func ProcessSsmCommandOnAccountRegion(
	ctx context.Context,
	wg *sync.WaitGroup,
	baseCfg aws.Config,
	appCfg *pkg.AppConfig,
	accountName string,
	roleToAssume string,
	commandToRun string,
	region string,
	instanceIDFromFlag string,
	successCounter *atomic.Int64,
	totalCounter *atomic.Int64,
) {
	defer wg.Done()

	accountID, accountExists := appCfg.Accounts[accountName]
	if !accountExists {
		pkg.LogVerbosef("ERROR: Account ID not found for SAWS config account name '%s'. Skipping.", accountName)
		return
	}

	assumedRoleCreds, err := pkg.AssumeRole(ctx, baseCfg, accountID, roleToAssume, "SsmCmdExecSess")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Assume Role Failed Account:%s Region:%s Role:%s: %v\n", accountName, region, roleToAssume, err)
		return
	}
	awsCreds := aws.Credentials{AccessKeyID: *assumedRoleCreds.AccessKeyId, SecretAccessKey: *assumedRoleCreds.SecretAccessKey, SessionToken: *assumedRoleCreds.SessionToken}

	var targetInstanceIDs []string
	if instanceIDFromFlag != "" {
		targetInstanceIDs = []string{instanceIDFromFlag}
		pkg.LogVerbosef("Targeting specific instance %s in Account %s, Region %s", instanceIDFromFlag, accountName, region)
	} else {
		instanceList, errList := GetSSMInstanceInfoList(ctx, awsCreds, region)
		if errList != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to list SSM instances for Account %s, Region %s: %v\n", accountName, region, errList)
			return
		}
		if len(instanceList) == 0 {
			pkg.LogVerbosef("No SSM-managed instances found in Account %s, Region %s. Skipping.", accountName, region)
			return
		}
		for _, info := range instanceList {
			if info.InstanceId != nil {
				targetInstanceIDs = append(targetInstanceIDs, *info.InstanceId)
			}
		}
		pkg.LogVerbosef("Found %d instances to target in Account %s, Region %s", len(targetInstanceIDs), accountName, region)
	}

	totalCounter.Add(int64(len(targetInstanceIDs)))

	var instanceWg sync.WaitGroup
	for _, instanceID := range targetInstanceIDs {
		instanceWg.Add(1)
		go executeSsmCommandOnInstance(ctx, &instanceWg, awsCreds, region, accountName, instanceID, commandToRun, successCounter)
	}
	instanceWg.Wait()
}

func executeSsmCommandOnInstance(
	ctx context.Context,
	wg *sync.WaitGroup,
	creds aws.Credentials,
	region, accountName, instanceID, command string,
	successCounter *atomic.Int64,
) {
	defer wg.Done()
	startTime := time.Now()

	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) { return creds, nil })),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to load SDK config for SSM command on %s: %v\n", instanceID, err)
		return
	}
	ssmClient := ssm.NewFromConfig(cfg)

	sendCmdOutput, err := ssmClient.SendCommand(ctx, &ssm.SendCommandInput{
		InstanceIds:    []string{instanceID},
		DocumentName:   aws.String("AWS-RunShellScript"),
		Parameters:     map[string][]string{"commands": {command}},
		TimeoutSeconds: aws.Int32(300),
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: Failed to send command to instance %s: %v\n", instanceID, err)
		return
	}
	commandID := *sendCmdOutput.Command.CommandId

	// Wait for command to complete
	var invocationOutput *ssm.GetCommandInvocationOutput
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(os.Stderr, "ERROR: Context cancelled while waiting for command on %s\n", instanceID)
			return
		case <-time.After(5 * time.Second): // Polling interval
		}

		invocationOutput, err = ssmClient.GetCommandInvocation(ctx, &ssm.GetCommandInvocationInput{
			CommandId:  aws.String(commandID),
			InstanceId: aws.String(instanceID),
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: Failed to get command invocation for instance %s: %v\n", instanceID, err)
			return
		}

		status := invocationOutput.Status
		if status == ssmtypes.CommandInvocationStatusSuccess ||
			status == ssmtypes.CommandInvocationStatusFailed ||
			status == ssmtypes.CommandInvocationStatusCancelled ||
			status == ssmtypes.CommandInvocationStatusTimedOut ||
			status == ssmtypes.CommandInvocationStatusCancelling {
			break
		}
	}

	duration := time.Since(startTime)
	status := string(invocationOutput.Status)
	exitCode := int(invocationOutput.ResponseCode)

	fmt.Printf("--- Result (Account: %s, Region: %s, Instance: %s, Status: %s, Exit Code: %d, Duration: %s) ---\n",
		accountName, region, instanceID, status, exitCode, duration.Round(time.Millisecond))

	stdOutput := ""
	if invocationOutput.StandardOutputContent != nil {
		stdOutput = strings.TrimSpace(*invocationOutput.StandardOutputContent)
	}
	errOutput := ""
	if invocationOutput.StandardErrorContent != nil {
		errOutput = strings.TrimSpace(*invocationOutput.StandardErrorContent)
	}

	if stdOutput != "" {
		fmt.Println("[STDOUT]")
		fmt.Println(stdOutput)
	}
	if errOutput != "" {
		if exitCode != 0 {
			fmt.Println("[STDERR]")
		} else {
			fmt.Println("[STDERR (Exit Code 0)]")
		}
		fmt.Println(errOutput)
	}
	fmt.Println("--- End Result ---")

	if exitCode == 0 && invocationOutput.Status == ssmtypes.CommandInvocationStatusSuccess {
		successCounter.Add(1)
	}
}

package saws

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"

	"saws/internal/pkg"
)

// listEcsClusters fetches ECS cluster ARNs for the given context.
func listEcsClusters(ctx context.Context, credsaws aws.Credentials, region string) ([]string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) { return credsaws, nil })),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load SDK config for ECS list clusters: %w", err)
	}
	ecsClient := ecs.NewFromConfig(cfg)

	var clusterArns []string
	paginator := ecs.NewListClustersPaginator(ecsClient, &ecs.ListClustersInput{MaxResults: aws.Int32(100)})

	pkg.LogVerbosef("Fetching ECS clusters in region %s...", region) // Use pkg.
	pageNum := 0
	for paginator.HasMorePages() {
		pageNum++
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list ECS clusters (page %d): %w", pageNum, err)
		}
		clusterArns = append(clusterArns, page.ClusterArns...)
		pkg.LogVerbosef("Fetched page %d of clusters (%d this page).", pageNum, len(page.ClusterArns)) // Use pkg.
	}
	pkg.LogVerbosef("Finished fetching clusters. Total found: %d", len(clusterArns)) // Use pkg.
	sort.Strings(clusterArns)
	return clusterArns, nil
}

// listEcsTasks fetches running task ARNs for a given cluster.
func listEcsTasks(ctx context.Context, credsaws aws.Credentials, region, clusterArn string) ([]string, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) { return credsaws, nil })),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load SDK config for ECS list tasks: %w", err)
	}
	ecsClient := ecs.NewFromConfig(cfg)

	var taskArns []string
	paginator := ecs.NewListTasksPaginator(ecsClient, &ecs.ListTasksInput{
		Cluster:       aws.String(clusterArn),
		DesiredStatus: ecstypes.DesiredStatusRunning,
		MaxResults:    aws.Int32(100),
	})

	pkg.LogVerbosef("Fetching RUNNING ECS tasks in cluster %s...", clusterArn) // Use pkg.
	pageNum := 0
	for paginator.HasMorePages() {
		pageNum++
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list ECS tasks (page %d) for cluster %s: %w", pageNum, clusterArn, err)
		}
		taskArns = append(taskArns, page.TaskArns...)
		pkg.LogVerbosef("Fetched page %d of tasks (%d this page).", pageNum, len(page.TaskArns)) // Use pkg.
	}
	pkg.LogVerbosef("Finished fetching tasks for cluster %s. Total RUNNING found: %d", clusterArn, len(taskArns)) // Use pkg.
	sort.Strings(taskArns)
	return taskArns, nil
}

// describeEcsTasks gets detailed information for specific tasks.
func describeEcsTasks(ctx context.Context, credsaws aws.Credentials, region, clusterArn string, taskArns []string) ([]ecstypes.Task, error) {
	if len(taskArns) == 0 {
		return []ecstypes.Task{}, nil
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithCredentialsProvider(aws.CredentialsProviderFunc(func(ctx context.Context) (aws.Credentials, error) { return credsaws, nil })),
		awsconfig.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load SDK config for ECS describe tasks: %w", err)
	}
	ecsClient := ecs.NewFromConfig(cfg)

	var describedTasks []ecstypes.Task
	batchSize := 100
	for i := 0; i < len(taskArns); i += batchSize {
		end := i + batchSize
		if end > len(taskArns) {
			end = len(taskArns)
		}
		batch := taskArns[i:end]
		pkg.LogVerbosef("Describing batch of %d tasks (starting index %d)...", len(batch), i) // Use pkg.
		output, err := ecsClient.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(clusterArn), Tasks: batch})
		if err != nil {
			return nil, fmt.Errorf("failed to describe ECS tasks batch (starting index %d): %w", i, err)
		}
		if len(output.Failures) > 0 {
			for _, failure := range output.Failures {
				reason := "N/A"
				if failure.Reason != nil {
					reason = *failure.Reason
				}
				arn := "N/A"
				if failure.Arn != nil {
					arn = *failure.Arn
				}
				pkg.LogVerbosef("Warning: Failed to describe task %s: %s", arn, reason) // Use pkg.
			}
		}
		describedTasks = append(describedTasks, output.Tasks...)
	}
	pkg.LogVerbosef("Finished describing tasks. Total described: %d", len(describedTasks)) // Use pkg.
	return describedTasks, nil
}

// HandleEcsExecSession handles the logic for the -ecs mode. Exported.
func HandleEcsExecSession(
	ctx context.Context,
	appCfg *pkg.AppConfig, // Use pkg.AppConfig
	clusterFlag, taskFlag, containerFlag, commandFlag, // Flags specific to ECS mode
	accountSelectorFlag, roleFlag, regionFlagFromCmd string, // Common context flags
) error {

	pkg.LogVerbosef("Preparing for ECS exec session...")                                                                                   // Use pkg.
	sCtx, creds, err := pkg.EstablishAWSContextAndAssumeRole(ctx, accountSelectorFlag, roleFlag, regionFlagFromCmd, "ECSExecSessionSetup") // Use pkg.
	if err != nil {
		return fmt.Errorf("could not establish AWS context for ECS exec session: %w", err)
	}

	awsCreds := aws.Credentials{AccessKeyID: *creds.AccessKeyId, SecretAccessKey: *creds.SecretAccessKey, SessionToken: *creds.SessionToken, Source: "SawsAssumedRoleForECS"}

	targetCluster := clusterFlag
	targetTask := taskFlag
	targetContainer := containerFlag
	targetCommand := commandFlag
	if targetCommand == "" {
		targetCommand = "/bin/sh"
		pkg.LogVerbosef("No command specified via --command flag, defaulting to %s", targetCommand) // Use pkg.
	}

	// --- Cluster Selection ---
	if targetCluster == "" {
		clusters, errList := listEcsClusters(ctx, awsCreds, sCtx.Region)
		if errList != nil {
			return fmt.Errorf("failed to list ECS clusters: %w", errList)
		}
		if len(clusters) == 0 {
			fmt.Fprintf(os.Stderr, "No ECS clusters found in Account %s, Region %s.\n", sCtx.AccountID, sCtx.Region)
			return nil
		}

		clusterNames := make([]string, len(clusters))
		clusterArnToName := make(map[string]string)
		for i, arn := range clusters {
			parts := strings.Split(arn, "/")
			name := parts[len(parts)-1]
			clusterNames[i] = name
			clusterArnToName[name] = arn
		}
		sort.Strings(clusterNames)

		chosenClusterName := ""
		prompt := &survey.Select{Message: "Choose ECS Cluster:", Options: clusterNames, PageSize: 15}
		errSurvey := survey.AskOne(prompt, &chosenClusterName, survey.WithValidator(survey.Required))
		if errSurvey != nil {
			return fmt.Errorf("cluster selection failed: %w", errSurvey)
		}
		targetCluster = clusterArnToName[chosenClusterName]    // Use Name or ARN? API needs name/ARN. Let's use the name for now, assuming it's unique or the API handles it.
		pkg.LogVerbosef("Selected cluster: %s", targetCluster) // Use pkg.
	} else {
		pkg.LogVerbosef("Using cluster '%s' provided via --cluster flag.", targetCluster) // Use pkg.
	}

	// --- Task Selection ---
	if targetTask == "" {
		tasks, errList := listEcsTasks(ctx, awsCreds, sCtx.Region, targetCluster)
		if errList != nil {
			return fmt.Errorf("failed to list ECS tasks for cluster %s: %w", targetCluster, errList)
		}
		if len(tasks) == 0 {
			fmt.Fprintf(os.Stderr, "No running ECS tasks found in cluster %s.\n", targetCluster)
			return nil
		}

		describedTasks, errDesc := describeEcsTasks(ctx, awsCreds, sCtx.Region, targetCluster, tasks)
		if errDesc != nil {
			pkg.LogVerbosef("Warning: failed to describe tasks, selection prompt will only show ARNs: %v", errDesc)
		} // Use pkg.

		taskOptions := make([]string, len(tasks))
		optionToTaskArn := make(map[string]string)
		taskInfoMap := make(map[string]ecstypes.Task)
		for _, task := range describedTasks {
			if task.TaskArn != nil {
				taskInfoMap[*task.TaskArn] = task
			}
		}

		for i, arn := range tasks {
			displayStr := arn
			taskID := strings.Split(arn, "/")[len(strings.Split(arn, "/"))-1]
			if detailedTask, ok := taskInfoMap[arn]; ok {
				defArn := "N/A"
				if detailedTask.TaskDefinitionArn != nil {
					defArn = *detailedTask.TaskDefinitionArn
				}
				defName := strings.Split(defArn, "/")[len(strings.Split(defArn, "/"))-1]
				createdAt := "N/A"
				if detailedTask.CreatedAt != nil {
					createdAt = detailedTask.CreatedAt.Local().Format("15:04:05")
				}
				displayStr = fmt.Sprintf("%s | %s | %s", taskID, defName, createdAt)
			}
			taskOptions[i] = displayStr
			optionToTaskArn[displayStr] = arn
		}

		chosenDisplayStr := ""
		prompt := &survey.Select{Message: "Choose Running Task:", Options: taskOptions, PageSize: 15}
		errSurvey := survey.AskOne(prompt, &chosenDisplayStr, survey.WithValidator(survey.Required))
		if errSurvey != nil {
			return fmt.Errorf("task selection failed: %w", errSurvey)
		}
		targetTask = optionToTaskArn[chosenDisplayStr]
		pkg.LogVerbosef("Selected task ARN: %s", targetTask) // Use pkg.
	} else {
		pkg.LogVerbosef("Using task '%s' provided via --task flag.", targetTask) // Use pkg.
	}

	// --- Container Selection ---
	if targetContainer == "" {
		var selectedTaskDetails *ecstypes.Task
		describedTasks, errDesc := describeEcsTasks(ctx, awsCreds, sCtx.Region, targetCluster, []string{targetTask})
		if errDesc != nil || len(describedTasks) == 0 {
			return fmt.Errorf("failed to describe selected task %s to list containers: %w", targetTask, errDesc)
		}
		selectedTaskDetails = &describedTasks[0]

		if len(selectedTaskDetails.Containers) == 0 {
			return fmt.Errorf("selected task %s has no containers listed", targetTask)
		}
		if len(selectedTaskDetails.Containers) == 1 {
			if selectedTaskDetails.Containers[0].Name != nil {
				targetContainer = *selectedTaskDetails.Containers[0].Name
				pkg.LogVerbosef("Auto-selected the only container in the task: %s", targetContainer) // Use pkg.
			} else {
				return fmt.Errorf("the only container in task %s has no name", targetTask)
			}
		} else {
			containerNames := []string{}
			for _, c := range selectedTaskDetails.Containers {
				if c.Name != nil {
					runtimeId := "N/A"
					if c.RuntimeId != nil {
						runtimeId = *c.RuntimeId
					}
					status := "N/A"
					if c.LastStatus != nil {
						status = *c.LastStatus
					}
					if runtimeId != "N/A" { // Only show running containers
						displayName := fmt.Sprintf("%s (%s)", *c.Name, status)
						containerNames = append(containerNames, displayName)
					}
				}
			}
			if len(containerNames) == 0 {
				return fmt.Errorf("no running containers found within task %s", targetTask)
			}
			if len(containerNames) == 1 {
				targetContainer = strings.Split(containerNames[0], " ")[0]
				pkg.LogVerbosef("Auto-selected the only running container in the task: %s", targetContainer) // Use pkg.
			} else {
				chosenContainerDisplay := ""
				prompt := &survey.Select{Message: "Choose Container:", Options: containerNames, PageSize: 10}
				errSurvey := survey.AskOne(prompt, &chosenContainerDisplay, survey.WithValidator(survey.Required))
				if errSurvey != nil {
					return fmt.Errorf("container selection failed: %w", errSurvey)
				}
				targetContainer = strings.Split(chosenContainerDisplay, " ")[0]
				pkg.LogVerbosef("Selected container: %s", targetContainer) // Use pkg.
			}
		}
	} else {
		pkg.LogVerbosef("Using container '%s' provided via --container flag.", targetContainer) // Use pkg.
	}

	if targetContainer == "" {
		return errors.New("could not determine target container")
	}

	// --- Execute Command ---
	awsCLIPath, err := exec.LookPath("aws")
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error: AWS CLI ('aws') not found in PATH. Required for ECS Exec.")
		fmt.Fprintln(os.Stderr, "Please install AWS CLI and ensure prerequisites for ecs execute-command are met.")
		return errors.New("aws cli not found")
	}
	pkg.LogVerbosef("Using AWS CLI at: %s", awsCLIPath)              // Use pkg.
	pkg.LogVerbosef("Preparing environment for ECS exec command...") // Use pkg.
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

	fmt.Fprintf(os.Stderr, "Starting ECS exec session...\n")
	fmt.Fprintf(os.Stderr, "  Cluster: %s\n", targetCluster)
	fmt.Fprintf(os.Stderr, "  Task:    %s\n", targetTask)
	fmt.Fprintf(os.Stderr, "  Container: %s\n", targetContainer)
	fmt.Fprintf(os.Stderr, "  Command: %s\n", targetCommand)
	if creds.Expiration != nil {
		fmt.Fprintf(os.Stderr, "  Context: Account=%s(%s), Role=%s. Session expires around: %s\n", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName, creds.Expiration.Local().Format(time.RFC1123))
	} else {
		fmt.Fprintf(os.Stderr, "  Context: Account=%s(%s), Role=%s. Session expiration time not available.\n", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName)
	}
	fmt.Fprintln(os.Stderr, "Ensure prerequisites for ECS execute-command are met (SSM agent, IAM permissions, etc.). Type 'exit' or Ctrl+D to end session.")

	ecsCmd := exec.Command(awsCLIPath, "ecs", "execute-command", "--cluster", targetCluster, "--task", targetTask, "--container", targetContainer, "--command", targetCommand, "--interactive", "--region", sCtx.Region)
	ecsCmd.Env = newEnv
	ecsCmd.Stdin = os.Stdin
	ecsCmd.Stdout = os.Stdout
	ecsCmd.Stderr = os.Stderr
	err = ecsCmd.Run()
	pkg.LogVerbosef("ECS exec session ended.") // Use pkg.
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			pkg.LogVerbosef("ECS exec command exited with status: %s.", exitErr.Error()) // Use pkg.
		} else {
			return fmt.Errorf("failed to run 'aws ecs execute-command': %w", err)
		}
	}
	return nil
}

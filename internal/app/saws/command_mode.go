package saws

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"saws/internal/pkg"

	"github.com/aws/aws-sdk-go-v2/aws"
)

func ProcessAccountRegion(
	ctx context.Context,
	wg *sync.WaitGroup,
	baseCfg aws.Config,
	appCfg *pkg.AppConfig,
	accountName string,
	roleToAssume string,
	commandToRun string,
	region string,
	successCounter *atomic.Int64,
) {
	defer wg.Done()

	accountID, accountExists := appCfg.Accounts[accountName]
	if !accountExists {
		log.Printf("ERROR: Account ID not found for SAWS config account name '%s'. Skipping.", accountName)
		return
	}

	assumedRoleCreds, err := pkg.AssumeRole(ctx, baseCfg, accountID, roleToAssume, "CmdExecSess")
	if err != nil {
		log.Printf("ERROR: Assume Role Failed Account:%s Region:%s Role:%s: %v", accountName, region, roleToAssume, err)
		return
	}

	cmd := exec.CommandContext(ctx, "bash", "-c", commandToRun)

	var cleanEnv []string
	originalEnv := os.Environ()
	for _, envVar := range originalEnv {
		if !strings.HasPrefix(envVar, "AWS_PROFILE=") &&
			!strings.HasPrefix(envVar, "AWS_ACCESS_KEY_ID=") &&
			!strings.HasPrefix(envVar, "AWS_SECRET_ACCESS_KEY=") &&
			!strings.HasPrefix(envVar, "AWS_SESSION_TOKEN=") &&
			!strings.HasPrefix(envVar, "AWS_SECURITY_TOKEN=") &&
			!strings.HasPrefix(envVar, "AWS_REGION=") &&
			!strings.HasPrefix(envVar, "AWS_DEFAULT_REGION=") &&
			!strings.HasPrefix(envVar, "AWS_CONFIG_FILE=") &&
			!strings.HasPrefix(envVar, "AWS_SHARED_CREDENTIALS_FILE=") {
			cleanEnv = append(cleanEnv, envVar)
		}
	}
	cmd.Env = cleanEnv
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", *assumedRoleCreds.AccessKeyId))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", *assumedRoleCreds.SecretAccessKey))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_SESSION_TOKEN=%s", *assumedRoleCreds.SessionToken))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_REGION=%s", region))
	cmd.Env = append(cmd.Env, fmt.Sprintf("AWS_DEFAULT_REGION=%s", region))

	var outb, errb bytes.Buffer
	cmd.Stdout = &outb
	cmd.Stderr = &errb

	startTime := time.Now()
	err = cmd.Run()
	duration := time.Since(startTime)

	exitCode := 0
	status := "SUCCESS"
	if err != nil {
		status = "FAILED"
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			log.Printf("ERROR executing command '%s' for Account: %s, Region: %s: %v", commandToRun, accountName, region, err)
			exitCode = -1
		}
	}

	fmt.Printf("--- Result (Account: %s, Region: %s, Status: %s, Exit Code: %d, Duration: %s) ---\n",
		accountName, region, status, exitCode, duration.Round(time.Millisecond))
	stdOutput := strings.TrimSpace(outb.String())
	errOutput := strings.TrimSpace(errb.String())
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

	if exitCode == 0 {
		successCounter.Add(1)
	}
}

package saws

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"saws/internal/pkg"

	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
)

func StartInteractiveSubShell(sCtx *pkg.SelectedContext, creds *ststypes.Credentials) error {
	pkg.LogVerbosef("Preparing interactive sub-shell environment...")
	currentEnv := os.Environ()
	newEnv := []string{}

	for _, e := range currentEnv {
		if !strings.HasPrefix(e, "AWS_ACCESS_KEY_ID=") &&
			!strings.HasPrefix(e, "AWS_SECRET_ACCESS_KEY=") &&
			!strings.HasPrefix(e, "AWS_SESSION_TOKEN=") &&
			!strings.HasPrefix(e, "AWS_SECURITY_TOKEN=") &&
			!strings.HasPrefix(e, "AWS_REGION=") &&
			!strings.HasPrefix(e, "AWS_DEFAULT_REGION=") &&
			!strings.HasPrefix(e, "AWS_PROFILE=") &&
			!strings.HasPrefix(e, "SAWS_INFO_") {
			newEnv = append(newEnv, e)
		}
	}

	newEnv = append(newEnv, fmt.Sprintf("AWS_ACCESS_KEY_ID=%s", *creds.AccessKeyId))
	newEnv = append(newEnv, fmt.Sprintf("AWS_SECRET_ACCESS_KEY=%s", *creds.SecretAccessKey))
	newEnv = append(newEnv, fmt.Sprintf("AWS_SESSION_TOKEN=%s", *creds.SessionToken))
	newEnv = append(newEnv, fmt.Sprintf("AWS_REGION=%s", sCtx.Region))
	newEnv = append(newEnv, fmt.Sprintf("AWS_DEFAULT_REGION=%s", sCtx.Region))

	newEnv = append(newEnv, fmt.Sprintf("SAWS_INFO_ACCOUNT_NAME=%s", sCtx.AccountName))
	newEnv = append(newEnv, fmt.Sprintf("SAWS_INFO_ACCOUNT_ID=%s", sCtx.AccountID))
	newEnv = append(newEnv, fmt.Sprintf("SAWS_INFO_ROLE_NAME=%s", sCtx.RoleName))
	newEnv = append(newEnv, fmt.Sprintf("SAWS_INFO_REGION=%s", sCtx.Region))

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "bash"
		pkg.LogVerbosef("SHELL environment variable not set, defaulting to %s for sub-shell", shell)
	}

	pkg.LogVerbosef("Starting interactive sub-shell: %s", shell)
	fmt.Fprintf(os.Stderr, "AWS context configured for: Account=%s(%s), Role=%s, Region=%s\n", sCtx.AccountName, sCtx.AccountID, sCtx.RoleName, sCtx.Region)
	if creds.Expiration != nil {
		fmt.Fprintf(os.Stderr, "Session expires around: %s\n", creds.Expiration.Local().Format(time.RFC1123))
	}
	fmt.Fprintln(os.Stderr, "Type 'exit' or press Ctrl+D to end this session.")

	cmd := exec.Command(shell)
	cmd.Env = newEnv
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	pkg.LogVerbosef("Interactive sub-shell session ended.")
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			pkg.LogVerbosef("Sub-shell exited with status: %s", exitErr.String())
		} else {
			return fmt.Errorf("failed to run interactive sub-shell '%s': %w", shell, err)
		}
	}
	return nil
}

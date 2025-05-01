# saws
`saws` is a command-line tool written in Go designed to simplify interactions with multiple AWS accounts and roles. It allows you to:

1.  **Execute AWS CLI commands** concurrently across multiple AWS accounts and regions after assuming a specified IAM role.
2.  **Interactively set up your shell environment** with temporary, role-based AWS credentials for a selected account, role, and region.

## Why `saws`?

Managing numerous AWS accounts (e.g., development, testing, production, security) can be cumbersome. Tasks like running compliance checks, inventorying resources, applying tags, or simply accessing resources often require assuming specific IAM roles within each account. `saws` streamlines these processes:

* **Efficiency:** Run commands in parallel across many accounts/regions instead of scripting loops manually.
* **Consistency:** Ensure commands are executed using the correct role and region context every time.
* **Security:** Promotes the use of short-lived, role-based credentials instead of relying on long-lived IAM user keys, aligning with AWS security best practices.
* **Simplicity:** Provides a straightforward way to obtain temporary credentials for interactive shell sessions without complex manual steps or heavyweight tools.
* **Configuration Driven:** Centralizes account, region, and common role definitions in an external YAML file, making management easier.

## Use Cases

`saws` is particularly useful for:

* **Platform Engineers, DevOps, SREs:** Managing infrastructure and running operational tasks across a multi-account AWS organization.
* **Security Teams:** Running compliance checks or security audits across the environment.
* **Developers:** Quickly assuming roles in different development or testing accounts.
* Anyone needing to perform repetitive tasks or gain temporary access within various AWS account/role contexts.

## Features

* Execute arbitrary AWS CLI commands across selected accounts and regions.
* Interactive mode (`-e`) to configure your shell environment with temporary credentials.
* External configuration via `saws-config.yaml` (default: `~/.aws/saws-config.yaml`).
* Supports selecting all accounts (`-a`) or a subset using patterns (`-s`).
* Supports specific region targeting (`-regions`) or automatic default region detection for command mode.
* Interactive selection of accounts, roles (from config file or manual input), and regions in environment setup mode.
* Concurrent execution for command mode, speeding up multi-account operations.

## Installation & Setup

### Prerequisites

* **Go:** Version 1.18 or later installed ([https://go.dev/doc/install](https://go.dev/doc/install)).
* **Git:** To clone the repository.
* **AWS CLI:** Required *only* if using the command execution mode (`-c`) ([https://aws.amazon.com/cli/](https://aws.amazon.com/cli/)). The environment setup mode (`-e`) does not require the AWS CLI itself, but you'll likely use the credentials with it afterward.

### Building from Source

1.  **Clone the repository:**
    ```bash
    git clone <your-git-repository-url>
    cd <repository-directory> # Directory containing saws.go
    ```
2.  **Install Dependencies:**
    ```bash
    go mod tidy
    ```
3.  **Build the executable:**
    ```bash
    go build -o saws .
    ```
4.  **Add to PATH (Optional):** Move the compiled `saws` binary to a directory in your system's PATH for easier access (e.g., `/usr/local/bin` or `~/bin`).
    ```bash
    mv saws /usr/local/bin/
    # or
    # mkdir -p ~/bin && mv saws ~/bin/ # (Ensure ~/bin is in your $PATH)
    ```

### Configuration File (`saws-config.yaml`)

`saws` requires a configuration file named `saws-config.yaml`.

1.  **Location:** By default, `saws` looks for this file in the following locations, in order:
    * Path specified by the `-config <path>` flag.
    * `~/.aws/saws-config.yaml` (Recommended standard location)
    * `./saws-config.yaml` (In the current directory where you run `saws`)
2.  **Format:** The file uses YAML format.
3.  **Required Sections:**
    * `accounts`: A map where keys are friendly names (used for selection) and values are the corresponding AWS Account IDs (as strings).
    * `common_regions`: A list of valid AWS region strings used for interactive selection in environment setup mode (`-e`).
4.  **Optional Section:**
    * `roles`: A map where keys are friendly names (used for selection in `-e` mode) and values are the exact IAM Role names. If this section is missing or empty, `-e` mode will prompt for manual role name input.

**Example `saws-config.yaml`:**

```yaml
# ~/.aws/saws-config.yaml

# Account Name to AWS Account ID mapping (Required)
accounts:
  audit: "123456789123"
  company-data-acc: "000000000000"
  company-data-dev: "111111111111"
  company-data-prd: "123456789123"
  company-infra-dev: "121834792211"
  company-infra-prd: "123456789123"
  # ... add all other necessary accounts

# List of common regions for interactive selection in -e mode (Required)
common_regions:
  - "eu-west-1"
  - "eu-west-2"
  - "eu-central-1"
  - "us-east-1"
  - "us-west-2"
  - "ap-southeast-1"
  - "ap-southeast-2"

# Optional: Friendly Name to IAM Role Name mapping for interactive selection (-e mode)
# If this section is missing or empty, you will be prompted to type the role name manually.
roles:
  Admin: "OrganizationAccountAccessRole"
  ReadOnly: "ViewOnlyAccessRole"
  PowerUser: "PowerUserAccessRole"
  InfraAdmin: "InfrastructureAdministratorRole"
```
### Initial AWS Credentials

`saws` itself needs AWS credentials to make the initial `sts:AssumeRole` call. It uses the standard AWS SDK credential chain, looking for credentials associated with the `baseProfileForAssume` (which defaults to `"default"`). Ensure you have credentials configured for this profile, typically via:

* `~/.aws/credentials` file
* `~/.aws/config` file (if using profiles)
* Environment Variables (`AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`)
* IAM Role attached to an EC2 instance or ECS task.

The user or role associated with these initial credentials must have `sts:AssumeRole` permission for the target roles you intend to assume in the other accounts.

## Usage

`saws` operates in two main modes: Command Execution and Environment Setup.

### Command Execution Mode

This mode assumes a role in the target accounts/regions and executes a specified AWS CLI command.

**Syntax:**

```bash
saws -r <role_name> -c "<aws_command>" [-a | -s <selector>] [-regions <region1,...>] [-config <path>]
```
## Required Flags:

* `-r <role_name>`: The exact name of the IAM Role to assume in the target accounts.
* `-c "<aws_command>"`: The AWS CLI command to execute (enclose in quotes if it contains spaces).

**One of the following is also required:**

* `-a`: Execute the command in **ALL** accounts listed in `saws-config.yaml`.
* `-s <selector>`: Execute the command only in accounts whose friendly names match the selector. The selector can be:
    * A single name (e.g., `data-dev`).
    * A pattern with `*` wildcards (e.g., `infra-*`).
    * Multiple space-separated names/patterns enclosed in quotes (e.g., `"company-data-dev company-data-tst infra-*"`).

## Optional Flags:

* `-regions <region1,region2,...>`: Comma-separated list of AWS regions to run the command in. If omitted, `saws` attempts to use the default region from your AWS configuration/environment; if that's not found, it falls back to `eu-west-1`.
* `-config <path>`: Specify a custom path to the `saws-config.yaml` file.

## Examples:

**List S3 buckets in all `infra-*` accounts in `eu-central-1`:**

```bash
saws -r InfraAdmin -c "aws s3 ls" -s "company-infra-*" -regions eu-central-1
```
Describe VPCs in data-dev and data-tst accounts across two regions:
```
saws -r ReadOnly -c "aws ec2 describe-vpcs --query 'Vpcs[*].VpcId'" -s "company-data-dev company-data-tst" -regions "eu-west-1,us-east-1"
```
Check IAM users in all accounts using the default region:
```
saws -r Admin -c "aws iam list-users --output count" -a
```
## Environment Setup Mode (`-e`)

This mode interactively prompts you to select an account, role, and region, assumes the role, and then prints `export` commands to standard output.
### Syntax:

```bash
saws -e [-config <path>]
```
### Optional Flags:

* `-config <path>`: Specify a custom path to the `saws-config.yaml` file.

**Flags Ignored/Disallowed in this mode:** `-r`, `-c`, `-a`, `-s`, `-regions`.

### How it Works:

1.  Run `saws -e`.
2.  `saws` will prompt you to select an **Account** from the list defined in your `saws-config.yaml`.
3.  `saws` will then check if the `roles` map exists in your config:
    * If **yes**, it will prompt you to select a **Role** from the friendly names listed there.
    * If **no**, it will prompt you to manually type the exact **IAM Role Name** you want to assume.
4.  Finally, it will prompt you to select a **Region** from the `common_regions` list in your config.
5.  If successful, `saws` prints `export` commands (which you should apply to your shell).

### Example:
```
# Start interactive setup
saws -e

# Follow the prompts:
# ? Choose an AWS Account: Company-data-dev  [Use arrows and Enter]
# ? Choose Role to Assume: Admin     [Use arrows and Enter - shown if roles defined in config]
# ? Choose AWS Region: eu-west-1  [Use arrows and Enter]

# (If successful, saws prints status messages to stderr)
# 2025/05/01 07:40:45 Success! AWS credentials exported for role 'OrganizationAccountAccessRole' in account 'data-dev' (eu-west-1).
# 2025/05/01 07:40:45 Session expires around: Thu, 01 May 2025 08:40:45 CEST
# 2025/05/01 07:40:45 Run 'unset AWS_ACCESS_KEY_ID ...' or open a new terminal to clear.

# Now your shell has the temporary credentials:
aws sts get-caller-identity
aws s3 ls --region eu-west-1

# When finished, unset the credentials:
unset AWS_ACCESS_KEY_ID AWS_SECRET_ACCESS_KEY AWS_SESSION_TOKEN AWS_REGION AWS_DEFAULT_REGION
```

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

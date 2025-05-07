# saws (Super AWS)

`saws` is a Go CLI tool to streamline interactions with multiple AWS accounts and roles. It enables you to:

* Run AWS CLI commands concurrently across accounts/regions (`-c`).
* Start an interactive sub-shell with assumed role credentials (`-e`).
* Connect to EC2 instances via SSM Session Manager (`-ssm`).
* Access ECS containers via ECS Exec (`-ecs`).

## Core Benefit

`saws` simplifies managing numerous AWS accounts by making it easy to execute commands or start sessions with the correct role and region context, enhancing efficiency and security.

## Key Features

* **Multi-Account Command Execution (`-c`):** Run commands across many accounts/regions.
* **Interactive Sub-Shell (`-e`):** Get a new shell with temporary AWS credentials.
* **SSM Instance Sessions (`-ssm`):** Connect directly to EC2 instances.
* **ECS Container Exec (`-ecs`):** Access running ECS containers interactively.
* **Configuration-Driven:** Uses `saws-config.yaml` for accounts, regions, and friendly role names.
* **Flexible Selection:** Target all accounts or use name/wildcard selectors.
* **Interactive Prompts:** For account, role, and region selection when not specified by flags.

## Quick Start

1.  **Prerequisites:**
    * Go (version 1.18 or later).
    * AWS CLI: Required for `-c`, `-ssm`, and `-ecs` modes.
        * For SSM mode, the Session Manager plugin for AWS CLI is also needed.
        * For ECS mode, ensure ECS Exec prerequisites are met on your resources.

2.  **Build:**
    ```bash
    git clone <your-repository-url>/saws.git # Replace with your actual repository URL
    cd saws
    go build -o saws ./cmd/saws
    # Optional: sudo mv saws /usr/local/bin/  (to make it globally accessible)
    ```

3.  **Configure (`saws-config.yaml`):**
    Create a configuration file, typically at `~/.aws/saws-config.yaml`.
    **Example `saws-config.yaml`:**
    ```yaml
    accounts:
      dev-main: "111111111111"
      prod-data: "222222222222"
      # ... other accounts

    common_regions:
      - "us-east-1"
      - "eu-west-1"
      # ... other common regions

    # Optional: map friendly names to actual IAM role names
    roles:
      Admin: "OrganizationAccountAccessRole"
      Developer: "DeveloperAccessRole"
    ```
    Ensure your base AWS profile (usually `default`) has permissions to assume these roles.

## Basic Usage Examples

* **Execute a command across accounts:**
    ```bash
    # List S3 buckets in all 'dev-*' accounts using the 'Developer' role in 'us-east-1'
    saws -c "aws s3 ls" -r Developer -s "dev-*" -regions us-east-1
    ```

* **Start an interactive sub-shell:**
    ```bash
    saws -e

    OR
    
    saws -e -s prod-data -r Admin -region eu-west-1
    ```

* **Connect to an ECS container (interactively):**
    ```bash
    saws -ecs

    OR
    
    saws -ecs -s dev-main -r Developer -region us-east-1
    ```

* **Connect to an EC2 instance via SSM (directly):**
    ```bash
    saws -ssm

    OR
    
    saws -ssm -i i-0123456789abcdef0 -s prod-data -r Admin -region eu-west-1
    ```

For more detailed options and examples, refer to the full help message using `saws -h`.

## Contribute
In case that you are interested or thinking of a feature, feel free to make a PR or ask me to do so.
Copyright 2025 Hosein Yousefi yousefi.hosein.o@gmail.com


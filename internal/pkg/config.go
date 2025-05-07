package pkg

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

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
var VerboseMode bool

const (
	ConfigFileName = "saws-config.yaml"
	AWSConfigDir   = ".aws"
)

const (
	envRoleVar    = "SAWS_ROLE"
	envRegionVar  = "SAWS_REGION"
	envAccountVar = "SAWS_ACCOUNT"
)

func LogVerbosef(format string, v ...any) {
	if VerboseMode {
		log.Printf(format, v...)
	}
}

func LoadConfig(filePath string) (*AppConfig, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read SAWS config file '%s': %w", filePath, err)
	}
	var loadedAppConfig AppConfig
	loadedAppConfig.Accounts = make(map[string]string)
	loadedAppConfig.Roles = make(map[string]string)
	loadedAppConfig.CommonRegions = []string{}

	err = yaml.Unmarshal(data, &loadedAppConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML from SAWS config file '%s': %w", filePath, err)
	}

	if len(loadedAppConfig.Accounts) == 0 {
		return nil, fmt.Errorf("SAWS config validation failed: 'accounts' map cannot be empty in '%s'", filePath)
	}
	if len(loadedAppConfig.CommonRegions) == 0 {
		LogVerbosef("Warning: 'common_regions' list is empty in SAWS config '%s'. Region selection might be limited.", filePath)
	}
	if len(loadedAppConfig.Roles) == 0 {
		LogVerbosef("Info: 'roles' map is empty or missing in SAWS config '%s'. Roles must be provided via -r flag or %s env var for session modes, or selected manually.", filePath, envRoleVar)
	}

	accounts = loadedAppConfig.Accounts
	commonRegions = loadedAppConfig.CommonRegions
	roles = loadedAppConfig.Roles

	LogVerbosef("Loaded SAWS config: %d accounts, %d regions, %d roles from %s", len(accounts), len(commonRegions), len(roles), filePath)
	return &loadedAppConfig, nil
}

func FindConfigPath(configFileOverride string) (string, error) {
	if configFileOverride != "" {
		expandedPath := configFileOverride
		if strings.HasPrefix(configFileOverride, "~") {
			homeDir, errHome := os.UserHomeDir()
			if errHome == nil {
				expandedPath = filepath.Join(homeDir, configFileOverride[1:])
			} else {
				LogVerbosef("Warning: Could not expand '~' in config path '%s': %v", configFileOverride, errHome)
			}
		}
		if _, err := os.Stat(expandedPath); err == nil {
			LogVerbosef("Using specified SAWS config file: %s", expandedPath)
			return expandedPath, nil
		}
		return "", fmt.Errorf("specified SAWS config file '%s' (expanded to '%s') not found", configFileOverride, expandedPath)
	}

	homeDir, err := os.UserHomeDir()
	if err == nil {
		configPath := filepath.Join(homeDir, AWSConfigDir, ConfigFileName)
		if _, errStat := os.Stat(configPath); errStat == nil {
			return configPath, nil
		}
	} else {
		LogVerbosef("Warning: Could not determine home directory: %v. Cannot check default ~/%s/%s location.", err, AWSConfigDir, ConfigFileName)
	}

	configPathLocal := ConfigFileName
	if _, err := os.Stat(configPathLocal); err == nil {
		return configPathLocal, nil
	}

	return "", fmt.Errorf("SAWS configuration file ('%s') not found in standard locations (~/%s/%s, ./%s) and no -config flag provided",
		ConfigFileName, AWSConfigDir, ConfigFileName, ConfigFileName)
}

package main

import (
	"errors"
	"fmt"
	"github.com/go-co-op/gocron"
	"github.com/gosimple/slug"
	"github.com/jcwillox/emerald"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
	"time"
)

const (
	ConfigPath        = "/data/options.json"
	BackupPath        = "/backup"
	DefaultConfigPath = "/root/.config/rclone/rclone.conf"
)

var (
	Arrow = emerald.Color("->", "black")
)

var (
	config   = &Config{}
	boldCyan = emerald.ColorFunc("cyan+b")
	remotes  []string
)

type Config struct {
	Jobs         []JobConfig
	Flags        Flags
	ExtraFlags   []string `yaml:"extra_flags"`
	DryRun       bool     `yaml:"dry_run"`
	RunOnce      bool     `yaml:"run_once"`
	ConfigPath   string   `yaml:"config_path"`
	RcloneConfig string   `yaml:"rclone_config"`
	NoRename     bool     `yaml:"no_rename"`
	NoUnrename   bool     `yaml:"no_unrename"`
	LogLevel     string   `yaml:"log_level"`
}

type JobConfig struct {
	Name         string
	Schedule     string
	Command      string
	Source       string
	Sources      []string
	Destination  string
	Destinations []string
	Include      []string
	Exclude      []string
	Flags        Flags
	ExtraFlags   []string `yaml:"extra_flags"`
}

type Flags map[string]string

func (f *Flags) UnmarshalYAML(n *yaml.Node) error {
	type FlagsT Flags
	var content string
	err := n.Decode(&content)
	if err != nil {
		return err
	}
	return yaml.Unmarshal([]byte(content), (*FlagsT)(f))
}

type BackupConfig struct {
	Name string
	Slug string
}

func main() {
	// configure slug format
	slug.Lowercase = false
	slug.CustomSub = map[string]string{
		" ": "_",
		"(": "",
		")": "",
		"[": "",
		"]": "",
		":": "",
		",": "",
	}

	// load addon configuration
	var err error
	config, err = LoadConfig()
	if err != nil {
		Fatalln("failed to read or parse config", err)
	}

	// write rclone config from addon config
	if config.RcloneConfig != "" {
		err := os.WriteFile(DefaultConfigPath, []byte(config.RcloneConfig), 0666)
		if err != nil {
			Fatalln("failed to write config")
		}
		config.ConfigPath = DefaultConfigPath
	}

	// check rclone config exists
	if stat, _ := os.Stat(config.ConfigPath); stat == nil {
		Fatalln(
			"rclone config does not exist!" +
				"\nIf this is your first time starting this add-on ensure to" +
				"\ncreate a valid rclone configuration at \"" + config.ConfigPath + "\"",
		)
	} else {
		Infoln("rclone config found")
	}

	remotes, err = GetRcloneRemotes()
	if err != nil {
		Fatalln("failed to retrieve list of rclone remotes")
	}

	Infoln("checking job configs...")
	for i, job := range config.Jobs {
		if job.Source != "" {
			job.Sources = []string{job.Source}
		}
		if job.Destination != "" {
			job.Destinations = []string{job.Destination}
		}
		err := CheckJob(job)
		if err != nil {
			Fatalln(err)
		}
		config.Jobs[i] = job
	}

	if config.RunOnce {
		for _, job := range config.Jobs {
			if job.Schedule == "" {
				CreateJob(job)()
			}
		}
	} else {
		scheduler := gocron.NewScheduler(time.Local)

		// only run 1 job at a time to prevent issues with file locks
		scheduler.SetMaxConcurrentJobs(1, gocron.WaitMode)

		Infoln("scheduled jobs:")

		for _, job := range config.Jobs {
			if job.Schedule == "" {
				// schedule to run immediately
				_, err = scheduler.Every(1).Second().LimitRunsTo(1).Do(CreateJob(job))
			} else {
				_, err = scheduler.Cron(job.Schedule).Do(CreateJob(job))
			}

			if err != nil {
				Fatalln("failed to schedule job", "'"+job.Name+"'", err)
			}
			emerald.Println(JobInfo(job, job.Schedule, "@startup"))
		}

		scheduler.StartBlocking()
	}
}

func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath)
	if err != nil {
		return nil, err
	}
	config := &Config{}
	err = yaml.Unmarshal(data, config)
	if err != nil {
		return nil, err
	}
	return config, nil
}

func CheckJob(job JobConfig) error {
	if len(job.Sources) == 0 {
		return errors.New("at least 1 source must be specified")
	}
	for _, source := range job.Sources {
		if err := CheckRemote(source); err != nil {
			return err
		}
	}
	for _, destination := range job.Destinations {
		if err := CheckRemote(destination); err != nil {
			return err
		}
	}
	return nil
}

func CheckRemote(path string) error {
	parts := strings.SplitN(path, ":", 2)
	if len(parts) == 2 {
		remote := parts[0] + ":"
		if !ArrayContains(remotes, remote) {
			return fmt.Errorf("remote '%s' does not exist; configured remotes are [%s]", remote, remotes)
		}
	} else if len(parts) == 1 {
		// check local path exists
		if stat, err := os.Stat(parts[0]); stat == nil {
			return fmt.Errorf("local target '%s' does not exist; %v", parts[0], err)
		}
	}
	return nil
}

func JobInfo(job JobConfig, prefix string, defaultName string, sourceDest ...string) string {
	sb := strings.Builder{}
	if prefix != "" {
		sb.WriteString(prefix)
		sb.WriteRune(' ')
	}
	if job.Name != "" {
		sb.WriteString(emerald.Cyan + "\"" + job.Name + "\"" + emerald.Green + "; ")
	} else if defaultName != "" {
		sb.WriteString(defaultName)
		sb.WriteString("; ")
	}
	// allow overriding sources and destinations
	if len(sourceDest) > 0 {
		job.Sources = sourceDest[:1]
		if len(sourceDest) > 1 {
			job.Destinations = sourceDest[1:]
		}
	}
	// print sources
	for i, source := range job.Sources {
		sb.WriteString(HighlightRemote(source))
		if i < len(job.Sources)-1 {
			sb.WriteString(", ")
		}
	}
	// print destinations
	if len(job.Destinations) > 0 {
		sb.WriteRune(' ')
		sb.WriteString(Arrow)
		sb.WriteRune(' ')
		for i, destination := range job.Destinations {
			if len(job.Sources) > 1 {
				for j, source := range job.Sources {
					sb.WriteString(HighlightRemote(destination))
					sb.WriteString(emerald.HighlightPath(source))
					if j < len(job.Sources)-1 {
						sb.WriteString(", ")
					}
				}
			} else {
				sb.WriteString(HighlightRemote(destination))
			}
			if i < len(job.Destinations)-1 {
				sb.WriteString(", ")
			}
		}
	}
	return sb.String()
}

func HighlightRemote(path string) string {
	parts := strings.SplitN(path, ":", 2)
	if len(parts) == 2 {
		remote := parts[0] + ":"
		if parts[1] != "" {
			return emerald.Bold + emerald.Magenta + remote + emerald.HighlightPath(parts[1])
		}
		return emerald.Bold + emerald.Magenta + remote + emerald.Reset
	} else if len(parts) == 1 {
		// local path
		return emerald.HighlightPathStat(parts[0])
	}
	return ""
}

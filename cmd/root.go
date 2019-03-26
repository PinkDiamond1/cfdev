package cmd

import (
	"code.cloudfoundry.org/cfdev/driver"
	"code.cloudfoundry.org/cfdev/driver/hyperkit"
	"code.cloudfoundry.org/cfdev/driver/hyperv"
	"code.cloudfoundry.org/cfdev/driver/kvm"
	"code.cloudfoundry.org/cfdev/env"
	"code.cloudfoundry.org/cfdev/profiler"
	"code.cloudfoundry.org/cfdev/runner"
	"io"
	"net/http"
	"runtime"
	"strings"
	"time"

	"code.cloudfoundry.org/cfdev/cfanalytics"
	cfdevdClient "code.cloudfoundry.org/cfdev/cfdevd/client"
	b2 "code.cloudfoundry.org/cfdev/cmd/bosh"
	b3 "code.cloudfoundry.org/cfdev/cmd/catalog"
	b9 "code.cloudfoundry.org/cfdev/cmd/deploy-service"
	b4 "code.cloudfoundry.org/cfdev/cmd/download"
	b8 "code.cloudfoundry.org/cfdev/cmd/provision"
	b5 "code.cloudfoundry.org/cfdev/cmd/start"
	b6 "code.cloudfoundry.org/cfdev/cmd/stop"
	b7 "code.cloudfoundry.org/cfdev/cmd/telemetry"
	b1 "code.cloudfoundry.org/cfdev/cmd/version"
	"code.cloudfoundry.org/cfdev/config"
	"code.cloudfoundry.org/cfdev/host"
	"code.cloudfoundry.org/cfdev/metadata"
	"code.cloudfoundry.org/cfdev/provision"
	"code.cloudfoundry.org/cfdev/resource"
	"code.cloudfoundry.org/cfdev/resource/progress"
	"github.com/spf13/cobra"
)

type UI interface {
	Say(message string, args ...interface{})
	Writer() io.Writer
}

type AnalyticsClient interface {
	Event(event string, data ...map[string]interface{}) error
	PromptOptInIfNeeded(customMessage string) error
}

type Toggle interface {
	Defined() bool
	Enabled() bool
	CustomAnalyticsDefined() bool
	IsCustom() bool
	SetCFAnalyticsEnabled(value bool) error
	SetCustomAnalyticsEnabled(value bool) error
	GetProps() map[string]interface{}
	SetProp(k, v string) error
}

func NewRoot(exit chan struct{}, ui UI, config config.Config, analyticsClient AnalyticsClient, analyticsToggle Toggle) *cobra.Command {
	root := &cobra.Command{Use: "cf", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().Bool("help", false, "")
	root.PersistentFlags().Lookup("help").Hidden = true

	usageTemplate := strings.Replace(root.UsageTemplate(), "\n"+`Use "{{.CommandPath}} [command] --help" for more information about a command.`, "", -1)
	root.SetUsageTemplate(usageTemplate)

	var (
		writer         = ui.Writer()
		metaDataReader = metadata.New()
		driver         = newDriver(ui, config)
		analyticsD     = &cfanalytics.AnalyticsD{
			Config:       config,
			DaemonRunner: newDaemonRunner(config),
		}
		cache = &resource.Cache{
			Dir:       config.CacheDir,
			HttpDo:    http.DefaultClient.Do,
			Progress:  progress.New(writer),
			RetryWait: time.Second,
			Writer:    writer,
		}

		dev = &cobra.Command{
			Use:           "dev",
			Short:         "Start and stop a single vm CF deployment running on your workstation",
			SilenceUsage:  true,
			SilenceErrors: true,
		}

		version = &b1.Version{
			UI:             ui,
			Version:        config.CliVersion,
			BuildVersion:   config.BuildVersion,
			Config:         config,
			MetaDataReader: metaDataReader,
		}

		bosh = &b2.Bosh{
			Exit:      exit,
			UI:        ui,
			Config:    config,
			Analytics: analyticsClient,
		}

		catalog = &b3.Catalog{
			UI:     ui,
			Config: config,
		}

		download = &b4.Download{
			Exit:   exit,
			UI:     ui,
			Config: config,
			Env:    &env.Env{Config: config},
		}

		telemetryCmd = &b7.Telemetry{
			Config:          config,
			UI:              ui,
			Analytics:       analyticsClient,
			AnalyticsToggle: analyticsToggle,
			AnalyticsD:      analyticsD,
		}

		prvsn = &b8.Provision{
			Exit:           exit,
			UI:             ui,
			Provisioner:    provision.NewController(config),
			MetaDataReader: metaDataReader,
			Config:         config,
		}

		stop = &b6.Stop{
			Analytics:  analyticsClient,
			Host:       &host.Host{},
			AnalyticsD: analyticsD,
			Driver:     driver,
		}

		start = &b5.Start{
			Exit:            exit,
			UI:              ui,
			Config:          config,
			Cache:           cache,
			Env:             &env.Env{Config: config},
			Analytics:       analyticsClient,
			AnalyticsToggle: analyticsToggle,
			Host:            &host.Host{},
			Driver:          driver,
			AnalyticsD:      analyticsD,
			Provisioner:     provision.NewController(config),
			Provision:       prvsn,
			MetaDataReader:  metaDataReader,
			Stop:            stop,
			Profiler:        &profiler.SystemProfiler{},
		}

		deployService = &b9.DeployService{
			UI:             ui,
			Provisioner:    provision.NewController(config),
			MetaDataReader: metaDataReader,
			Analytics:      analyticsClient,
			Config:         config,
		}

		helpCmd = &cobra.Command{
			Use:   "help [command]",
			Short: "Help about any command",
			Run: func(c *cobra.Command, args []string) {
				cmd, _, _ := dev.Find(args)
				cmd.Help()
			},
		}
	)

	root.AddCommand(dev)
	dev.AddCommand(version.Cmd())
	dev.AddCommand(bosh.Cmd())
	dev.AddCommand(catalog.Cmd())
	dev.AddCommand(download.Cmd())
	dev.AddCommand(start.Cmd())
	dev.AddCommand(stop.Cmd())
	dev.AddCommand(telemetryCmd.Cmd())
	dev.AddCommand(prvsn.Cmd())
	dev.AddCommand(deployService.Cmd())
	dev.AddCommand(helpCmd)
	return root
}

func newDriver(ui UI, config config.Config) driver.Driver {
	daemonRunner := newDaemonRunner(config)

	switch runtime.GOOS {
	case "darwin":
		cfdevd := cfdevdClient.New("CFD3V", config.CFDevDSocketPath)

		return hyperkit.New(config, daemonRunner, ui, cfdevd)
	case "windows":
		return hyperv.New(
			config,
			daemonRunner,
			ui,
			runner.Powershell{},
			"7207f451-2ca3-4b88-8d01-820a21d78293",
			"cc2a519a-fb40-4e45-a9f1-c7f04c5ad7fa",
			"e3ae8f06-8c25-47fb-b6ed-c20702bcef5e",
		)
	case "linux":
		return kvm.New(config, daemonRunner, ui)
	}

	return nil
}
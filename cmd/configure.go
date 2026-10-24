package cmd

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"time"

	"github.com/AlecAivazis/survey/v2"
	"github.com/AlecAivazis/survey/v2/terminal"
	"github.com/spf13/cobra"

	"github.com/pterodactyl/wings/config"
)

var configureArgs struct {
	PanelURL      string
	Token         string
	ConfigPath    string
	Node          string
	Override      bool
	AllowInsecure bool
}

var nodeIdRegex = regexp.MustCompile(`^(\d+)$`)

var configureCmd = &cobra.Command{
	Use:   "configure",
	Short: "使用令牌自动配置 wings",
	Run:   configureCmdRun,
}

func init() {
	configureCmd.PersistentFlags().StringVarP(&configureArgs.PanelURL, "panel-url", "p", "", "The base URL for this daemon's panel")
	configureCmd.PersistentFlags().StringVarP(&configureArgs.Token, "token", "t", "", "The API key to use for fetching node information")
	configureCmd.PersistentFlags().StringVarP(&configureArgs.Node, "node", "n", "", "The ID of the node which will be connected to this daemon")
	configureCmd.PersistentFlags().StringVarP(&configureArgs.ConfigPath, "config-path", "c", config.DefaultLocation, "The path where the configuration file should be made")
	configureCmd.PersistentFlags().BoolVar(&configureArgs.Override, "override", false, "Set to true to override an existing configuration for this node")
	configureCmd.PersistentFlags().BoolVar(&configureArgs.AllowInsecure, "allow-insecure", false, "Set to true to disable certificate checking")
}

func configureCmdRun(cmd *cobra.Command, args []string) {
	if configureArgs.AllowInsecure {
		http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // opt-in via --allow-insecure
		}
	}

	// The configuration is written to --config-path, or to the global --config
	// path if only that was given.
	target := configureArgs.ConfigPath
	if cmd != nil && !cmd.Flags().Changed("config-path") && cmd.Flags().Changed("config") {
		target = configPath
	}

	if _, err := os.Stat(target); err == nil && !configureArgs.Override {
		// A failed or interrupted prompt leaves Override false, which aborts below.
		_ = survey.AskOne(&survey.Confirm{Message: "覆盖现有配置文件"}, &configureArgs.Override)
		if !configureArgs.Override {
			fmt.Println("正在中止进程；该节点的配置文件已存在。")
			os.Exit(1)
		}
	} else if err != nil && !os.IsNotExist(err) {
		panic(err)
	}

	var questions []*survey.Question
	if configureArgs.PanelURL == "" {
		questions = append(questions, &survey.Question{
			Name:   "PanelURL",
			Prompt: &survey.Input{Message: "Panel URL: "},
			Validate: func(ans interface{}) error {
				if str, ok := ans.(string); ok {
					_, err := url.ParseRequestURI(str)
					return err
				}
				return nil
			},
		})
	}

	if configureArgs.Token == "" {
		questions = append(questions, &survey.Question{
			Name:   "Token",
			Prompt: &survey.Input{Message: "API Token: "},
			Validate: func(ans interface{}) error {
				if str, ok := ans.(string); ok {
					if len(str) == 0 {
						return fmt.Errorf("请提供有效的身份验证令牌")
					}
				}
				return nil
			},
		})
	}

	if configureArgs.Node == "" {
		questions = append(questions, &survey.Question{
			Name:   "Node",
			Prompt: &survey.Input{Message: "Node ID: "},
			Validate: func(ans interface{}) error {
				if str, ok := ans.(string); ok {
					if !nodeIdRegex.Match([]byte(str)) {
						return fmt.Errorf("请提供有效的身份验证令牌")
					}
				}
				return nil
			},
		})
	}

	if err := survey.Ask(questions, &configureArgs); err != nil {
		if err == terminal.InterruptErr {
			return
		}

		panic(err)
	}

	c := &http.Client{
		Timeout: time.Second * 30,
	}

	req, err := getRequest()
	if err != nil {
		panic(err)
	}

	fmt.Println(req.URL.String())

	res, err := c.Do(req)
	if err != nil {
		fmt.Println("无法从面板获取配置。\n", err.Error())
		os.Exit(1)
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusUnauthorized {
		fmt.Println("提供的身份验证凭据无效。")
		os.Exit(1)
	} else if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)

		fmt.Println("处理此请求时发生错误。\n", string(b))
		os.Exit(1)
	}

	b, err := io.ReadAll(io.LimitReader(res.Body, maxConfigurationResponseSize))
	if err != nil {
		panic(err)
	}

	if err := writePanelConfiguration(target, configureArgs.PanelURL, b); err != nil {
		fmt.Println("Failed to configure wings.\n", err.Error())
		os.Exit(1)
	}

	fmt.Println("wings 配置成功。")
}

// maxConfigurationResponseSize is the largest node configuration that is read
// from the Panel.
const maxConfigurationResponseSize = 1 << 20

// writePanelConfiguration applies the node configuration returned by the Panel
// to the configuration file at path, keeping any other settings already in it.
func writePanelConfiguration(path string, panelURL string, body []byte) error {
	cfg, err := config.Load(path)
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = config.NewAtPath(path)
	}
	if err != nil {
		return err
	}

	// Only take the values from the Panel that it is allowed to set.
	p := cfg.PanelConfiguration()
	if err := json.Unmarshal(body, &p); err != nil {
		return err
	}
	cfg.ApplyPanelConfiguration(p)

	// Manually specify the Panel URL as it won't be decoded from JSON.
	cfg.PanelLocation = panelURL

	// The Panel returns the token itself.
	if err := cfg.ResolveToken(true); err != nil {
		return err
	}

	return config.WriteToDisk(cfg)
}

func getRequest() (*http.Request, error) {
	u, err := url.Parse(configureArgs.PanelURL)
	if err != nil {
		panic(err)
	}

	u.Path = path.Join(u.Path, fmt.Sprintf("api/application/nodes/%s/configuration", configureArgs.Node))

	r, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	r.Header.Set("Accept", "application/vnd.pterodactyl.v1+json")
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", fmt.Sprintf("Bearer %s", configureArgs.Token))

	return r, nil
}

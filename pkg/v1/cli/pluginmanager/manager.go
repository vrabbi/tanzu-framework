// Copyright 2021 VMware, Inc. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Package pluginmanager is resposible for plugin discovery and installation
package pluginmanager

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/pkg/errors"
	"go.uber.org/multierr"
	"golang.org/x/mod/semver"
	kerrors "k8s.io/apimachinery/pkg/util/errors"

	cliv1alpha1 "github.com/vmware-tanzu/tanzu-framework/apis/cli/v1alpha1"
	"github.com/vmware-tanzu/tanzu-framework/apis/config/v1alpha1"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/cli"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/cli/catalog"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/cli/common"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/cli/discovery"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/cli/plugin"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/config"
	"github.com/vmware-tanzu/tanzu-framework/pkg/v1/tkg/log"
)

const (
	// exe is an executable file extension
	exe = ".exe"
)

var execCommand = exec.Command

// ValidatePlugin validates the plugin descriptor.
func ValidatePlugin(p *cliv1alpha1.PluginDescriptor) (err error) {
	// skip builder plugin for bootstrapping
	if p.Name == "builder" {
		return nil
	}
	if p.Name == "" {
		err = multierr.Append(err, errors.New("plugin name cannot be empty"))
	}
	if p.Version == "" {
		err = multierr.Append(err, fmt.Errorf("plugin %q version cannot be empty", p.Name))
	}
	if !semver.IsValid(p.Version) && p.Version != "dev" {
		err = multierr.Append(err, fmt.Errorf("version %q %q is not a valid semantic version", p.Name, p.Version))
	}
	if p.Description == "" {
		err = multierr.Append(err, fmt.Errorf("plugin %q description cannot be empty", p.Name))
	}
	if p.Group == "" {
		err = multierr.Append(err, fmt.Errorf("plugin %q group cannot be empty", p.Name))
	}
	return
}

func discoverPlugins(pd []v1alpha1.PluginDiscovery) ([]plugin.Discovered, error) {
	allPlugins := make([]plugin.Discovered, 0)
	for _, d := range pd {
		discObject, err := discovery.CreateDiscoveryFromV1alpha1(d)
		if err != nil {
			return nil, errors.Wrapf(err, "unable to create discovery")
		}

		plugins, err := discObject.List()
		if err != nil {
			log.Warningf("unable to list plugin from discovery '%v': %v", discObject.Name(), err.Error())
			continue
		}
		allPlugins = append(allPlugins, plugins...)
	}
	return allPlugins, nil
}

// DiscoverStandalonePlugins returns the available standalone plugins
func DiscoverStandalonePlugins() (plugins []plugin.Discovered, err error) {
	cfg, e := config.GetClientConfig()
	if e != nil {
		err = errors.Wrapf(e, "unable to get client configuration")
		return
	}

	if cfg == nil || cfg.ClientOptions == nil || cfg.ClientOptions.CLI == nil {
		plugins = []plugin.Discovered{}
		return
	}

	plugins, err = discoverPlugins(cfg.ClientOptions.CLI.DiscoverySources)
	if err != nil {
		return
	}

	for i := range plugins {
		plugins[i].Scope = common.PluginScopeStandalone
		plugins[i].Status = common.PluginStatusNotInstalled
	}
	return
}

// DiscoverServerPlugins returns the available plugins associated with the given server
func DiscoverServerPlugins(serverName string) ([]plugin.Discovered, error) {
	plugins := []plugin.Discovered{}
	if serverName == "" {
		// If servername is not specified than returning empty list
		// as there are no server plugins that can be discovered
		return plugins, nil
	}

	discoverySources := config.GetDiscoverySources(serverName)
	plugins, err := discoverPlugins(discoverySources)
	if err != nil {
		return plugins, err
	}
	for i := range plugins {
		plugins[i].Scope = common.PluginScopeContext
		plugins[i].Status = common.PluginStatusNotInstalled
	}
	return plugins, nil
}

// DiscoverPlugins returns the available plugins that can be used with the given server
// If serverName is empty(""), return only standalone plugins
func DiscoverPlugins(serverName string) ([]plugin.Discovered, []plugin.Discovered) {
	serverPlugins, err := DiscoverServerPlugins(serverName)
	if err != nil {
		log.Warningf("unable to discover server plugins, %v", err.Error())
	}

	standalonePlugins, err := DiscoverStandalonePlugins()
	if err != nil {
		log.Warningf("unable to discover standalone plugins, %v", err.Error())
	}

	// TODO(anuj): Remove duplicate plugins with server plugins getting higher priority
	return serverPlugins, standalonePlugins
}

// AvailablePlugins returns the list of available plugins including discovered and installed plugins
// If serverName is empty(""), return only available standalone plugins
func AvailablePlugins(serverName string) ([]plugin.Discovered, error) {
	discoveredServerPlugins, discoveredStandalonePlugins := DiscoverPlugins(serverName)
	return availablePlugins(serverName, discoveredServerPlugins, discoveredStandalonePlugins)
}

// AvailablePluginsFromLocalSource returns the list of available plugins from local source
func AvailablePluginsFromLocalSource(localPath string) ([]plugin.Discovered, error) {
	localStandalonePlugins, err := DiscoverPluginsFromLocalSource(localPath)
	if err != nil {
		log.Warningf("unable to discover standalone plugins from local source, %v", err.Error())
	}
	return availablePlugins("", []plugin.Discovered{}, localStandalonePlugins)
}

func availablePlugins(serverName string, discoveredServerPlugins, discoveredStandalonePlugins []plugin.Discovered) ([]plugin.Discovered, error) {
	installedSeverPluginDesc, installedStandalonePluginDesc, err := InstalledPlugins(serverName)
	if err != nil {
		return nil, err
	}

	availablePlugins := availablePluginsFromStandaloneAndServerPlugins(discoveredServerPlugins, discoveredStandalonePlugins)

	setAvailablePluginsStatus(availablePlugins, installedSeverPluginDesc)
	setAvailablePluginsStatus(availablePlugins, installedStandalonePluginDesc)

	installedButNotDiscoveredPlugins := getInstalledButNotDiscoveredStandalonePlugins(availablePlugins, installedStandalonePluginDesc)
	availablePlugins = append(availablePlugins, installedButNotDiscoveredPlugins...)

	return availablePlugins, nil
}

func getInstalledButNotDiscoveredStandalonePlugins(availablePlugins []plugin.Discovered, installedPluginDesc []cliv1alpha1.PluginDescriptor) []plugin.Discovered {
	var newPlugins []plugin.Discovered
	for i := range installedPluginDesc {
		found := false
		for j := range availablePlugins {
			if installedPluginDesc[i].Name == availablePlugins[j].Name {
				found = true
				// If plugin is installed but marked as not installed as part of availablePlugins list
				// mark the plugin as installed
				// This is possible if user has used --local mode to install the plugin which is also
				// getting discovered from the configured discovery sources
				if availablePlugins[j].Status == common.PluginStatusNotInstalled {
					availablePlugins[j].Status = common.PluginStatusInstalled
				}
			}
		}
		if !found {
			p := DiscoveredFromPluginDescriptor(&installedPluginDesc[i])
			p.Scope = common.PluginScopeStandalone
			p.Status = common.PluginStatusInstalled
			newPlugins = append(newPlugins, p)
		}
	}
	return newPlugins
}

// DiscoveredFromPluginDescriptor returns discovered plugin object from k8sV1alpha1
func DiscoveredFromPluginDescriptor(p *cliv1alpha1.PluginDescriptor) plugin.Discovered {
	dp := plugin.Discovered{
		Name:               p.Name,
		Description:        p.Description,
		RecommendedVersion: p.Version,
		Source:             p.Discovery,
		SupportedVersions:  []string{p.Version},
	}
	return dp
}

func setAvailablePluginsStatus(availablePlugins []plugin.Discovered, installedPluginDesc []cliv1alpha1.PluginDescriptor) {
	for i := range installedPluginDesc {
		for j := range availablePlugins {
			if installedPluginDesc[i].Name == availablePlugins[j].Name {
				// Match found, Check for update available and update status
				if installedPluginDesc[i].Version == availablePlugins[j].RecommendedVersion {
					availablePlugins[j].Status = common.PluginStatusInstalled
				} else {
					availablePlugins[j].Status = common.PluginStatusUpdateAvailable
				}
			}
		}
	}
}

func availablePluginsFromStandaloneAndServerPlugins(discoveredServerPlugins, discoveredStandalonePlugins []plugin.Discovered) []plugin.Discovered {
	availablePlugins := discoveredServerPlugins

	// Check whether the default standalone discovery type is local or not
	isLocalStandaloneDiscovery := config.GetDefaultStandaloneDiscoveryType() == common.DiscoveryTypeLocal

	for i := range discoveredStandalonePlugins {
		matchIndex := pluginIndexForName(availablePlugins, discoveredStandalonePlugins[i].Name)

		// Add the standalone plugin to available plugins if it doesn't exist in the serverPlugins list
		// OR
		// Current standalone discovery or plugin discovered is of type 'local'
		// We are overriding the discovered plugins that we got from server in case of 'local' discovery type
		// to allow developers to use the plugins that are built locally and not returned from the server
		// This local discovery is only used for development purpose and should not be used for production
		if matchIndex < 0 {
			availablePlugins = append(availablePlugins, discoveredStandalonePlugins[i])
			continue
		}
		if isLocalStandaloneDiscovery || discoveredStandalonePlugins[i].DiscoveryType == common.DiscoveryTypeLocal { // matchIndex >= 0 is guaranteed here
			availablePlugins[matchIndex] = discoveredStandalonePlugins[i]
		}
	}
	return availablePlugins
}

func pluginIndexForName(availablePlugins []plugin.Discovered, pluginName string) int {
	for j := range availablePlugins {
		if pluginName == availablePlugins[j].Name {
			return j
		}
	}
	return -1 // haven't found a match
}

// InstalledPlugins returns the installed plugins.
// If serverName is empty(""), return only installed standalone plugins
func InstalledPlugins(serverName string, exclude ...string) (serverPlugins, standalonePlugins []cliv1alpha1.PluginDescriptor, err error) {
	var serverCatalog, standAloneCatalog *catalog.ContextCatalog

	if serverName != "" {
		serverCatalog, err = catalog.NewContextCatalog(serverName)
		if err != nil {
			return nil, nil, err
		}
		serverPlugins = serverCatalog.List()
	}

	standAloneCatalog, err = catalog.NewContextCatalog("")
	if err != nil {
		return nil, nil, err
	}
	standalonePlugins = standAloneCatalog.List()
	return
}

// DescribePlugin describes a plugin.
// If serverName is empty(""), only consider standalone plugins
func DescribePlugin(serverName, pluginName string) (desc *cliv1alpha1.PluginDescriptor, err error) {
	c, err := catalog.NewContextCatalog(serverName)
	if err != nil {
		return nil, err
	}
	descriptor, ok := c.Get(pluginName)
	if !ok {
		err = fmt.Errorf("could not get plugin path for plugin %q", pluginName)
	}

	return &descriptor, err
}

// InitializePlugin initializes the plugin configuration
// If serverName is empty(""), only consider standalone plugins
func InitializePlugin(serverName, pluginName string) error {
	c, err := catalog.NewContextCatalog(serverName)
	if err != nil {
		return err
	}
	descriptor, ok := c.Get(pluginName)
	if !ok {
		return errors.Wrapf(err, "could not get plugin path for plugin %q", pluginName)
	}

	b, err := execCommand(descriptor.InstallationPath, "post-install").CombinedOutput()

	// Note: If user is installing old version of plugin than it is possible that
	// the plugin does not implement post-install command. Ignoring the
	// errors if the command does not exist for a particular plugin.
	if err != nil && !strings.Contains(string(b), "unknown command") {
		log.Warningf("Warning: Failed to initialize plugin '%q' after installation. %v", pluginName, string(b))
	}

	return nil
}

// InstallPlugin installs a plugin from the given repository.
// If serverName is empty(""), only consider standalone plugins
func InstallPlugin(serverName, pluginName, version string) error {
	availablePlugins, err := AvailablePlugins(serverName)
	if err != nil {
		return err
	}
	for i := range availablePlugins {
		if availablePlugins[i].Name == pluginName {
			if availablePlugins[i].Scope == common.PluginScopeStandalone {
				serverName = ""
			}
			return installOrUpgradePlugin(serverName, &availablePlugins[i], version)
		}
	}

	return errors.Errorf("unable to find plugin '%v'", pluginName)
}

// UpgradePlugin upgrades a plugin from the given repository.
// If serverName is empty(""), only consider standalone plugins
func UpgradePlugin(serverName, pluginName, version string) error {
	availablePlugins, err := AvailablePlugins(serverName)
	if err != nil {
		return err
	}
	for i := range availablePlugins {
		if availablePlugins[i].Name == pluginName {
			if availablePlugins[i].Scope == common.PluginScopeStandalone {
				serverName = ""
			}
			return installOrUpgradePlugin(serverName, &availablePlugins[i], version)
		}
	}

	return errors.Errorf("unable to find plugin '%v'", pluginName)
}

// GetRecommendedVersionOfPlugin returns recommended version of the plugin
// If serverName is empty(""), only consider standalone plugins
func GetRecommendedVersionOfPlugin(serverName, pluginName string) (string, error) {
	availablePlugins, err := AvailablePlugins(serverName)
	if err != nil {
		return "", err
	}
	for i := range availablePlugins {
		if availablePlugins[i].Name == pluginName {
			return availablePlugins[i].RecommendedVersion, nil
		}
	}
	return "", errors.Errorf("unable to find plugin '%v'", pluginName)
}

func installOrUpgradePlugin(serverName string, p *plugin.Discovered, version string) error {
	log.Infof("Installing plugin '%v:%v'", p.Name, version)

	b, err := p.Distribution.Fetch(version, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	pluginName := p.Name
	pluginPath := filepath.Join(common.DefaultPluginRoot, pluginName, version)

	err = os.MkdirAll(filepath.Dir(pluginPath), os.ModePerm)
	if err != nil {
		return err
	}

	if common.BuildArch().IsWindows() {
		pluginPath += exe
	}

	err = os.WriteFile(pluginPath, b, 0755)
	if err != nil {
		return errors.Wrap(err, "could not write file")
	}

	b, err = execCommand(pluginPath, "info").Output()
	if err != nil {
		return errors.Wrapf(err, "could not describe plugin %q", pluginName)
	}
	var descriptor cliv1alpha1.PluginDescriptor
	err = json.Unmarshal(b, &descriptor)
	if err != nil {
		return errors.Wrapf(err, "could not unmarshal plugin %q description", pluginName)
	}
	descriptor.InstallationPath = pluginPath
	descriptor.Discovery = p.Source

	c, err := catalog.NewContextCatalog(serverName)
	if err != nil {
		return err
	}
	err = c.Upsert(&descriptor)
	if err != nil {
		log.Info("Plugin descriptor could not be updated in cache")
	}
	err = InitializePlugin(serverName, pluginName)
	if err != nil {
		log.Infof("could not initialize plugin after installing: %v", err.Error())
	}
	return nil
}

// DeletePlugin deletes a plugin.
// If serverName is empty(""), only consider standalone plugins
func DeletePlugin(serverName, pluginName string) error {
	c, err := catalog.NewContextCatalog(serverName)
	if err != nil {
		return err
	}
	_, ok := c.Get(pluginName)
	if !ok {
		return fmt.Errorf("could not get plugin path for plugin %q", pluginName)
	}

	err = c.Delete(pluginName)
	if err != nil {
		return fmt.Errorf("plugin %q could not be deleted from cache", pluginName)
	}

	// TODO: delete the plugin binary if it is not used by any server

	return nil
}

// SyncPlugins automatically downloads all available plugins to users machine
// If serverName is empty(""), only sync standalone plugins
func SyncPlugins(serverName string) error {
	log.Info("Checking for required plugins...")
	plugins, err := AvailablePlugins(serverName)
	if err != nil {
		return err
	}

	installed := false

	errList := make([]error, 0)
	for idx := range plugins {
		if plugins[idx].Status != common.PluginStatusInstalled {
			installed = true
			err = InstallPlugin(serverName, plugins[idx].Name, plugins[idx].RecommendedVersion)
			if err != nil {
				errList = append(errList, err)
			}
		}
	}
	err = kerrors.NewAggregate(errList)
	if err != nil {
		return err
	}

	if !installed {
		log.Info("All required plugins are already installed and up-to-date")
	} else {
		log.Info("Successfully installed all required plugins")
	}
	return nil
}

// InstallPluginsFromLocalSource installs plugin from local source directory
func InstallPluginsFromLocalSource(pluginName, version, localPath string) error {
	// Set default local plugin distro to localpath as while installing the plugin
	// from local source we should take t
	common.DefaultLocalPluginDistroDir = localPath

	plugins, err := DiscoverPluginsFromLocalSource(localPath)
	if err != nil {
		return err
	}

	found := false

	errList := make([]error, 0)
	for idx := range plugins {
		if pluginName == cli.AllPlugins || pluginName == plugins[idx].Name {
			found = true
			err := installOrUpgradePlugin("", &plugins[idx], plugins[idx].RecommendedVersion)
			if err != nil {
				errList = append(errList, err)
			}
		}
	}
	err = kerrors.NewAggregate(errList)
	if err != nil {
		return err
	}
	if !found {
		return errors.Errorf("unable to find plugin '%v'", pluginName)
	}
	return nil
}

// DiscoverPluginsFromLocalSource returns the available plugins that are discovered from the provided local path
func DiscoverPluginsFromLocalSource(localPath string) ([]plugin.Discovered, error) {
	if localPath == "" {
		return nil, nil
	}

	// Set default local plugin distro to localpath while installing the plugin
	// from local source. This is done to allow CLI to know the basepath incase the
	// relative path is provided as part of CLIPlugin definition for local discovery
	common.DefaultLocalPluginDistroDir = localPath

	var pds []v1alpha1.PluginDiscovery

	items, err := os.ReadDir(filepath.Join(localPath, "discovery"))
	if err != nil {
		return nil, errors.Wrapf(err, "error while reading local plugin manifest directory")
	}
	for _, item := range items {
		if item.IsDir() {
			pd := v1alpha1.PluginDiscovery{
				Local: &v1alpha1.LocalDiscovery{
					Name: "",
					Path: filepath.Join(localPath, "discovery", item.Name()),
				},
			}
			pds = append(pds, pd)
		}
	}

	plugins, err := discoverPlugins(pds)
	if err != nil {
		return nil, err
	}

	for i := range plugins {
		plugins[i].Scope = common.PluginScopeStandalone
		plugins[i].Status = common.PluginStatusNotInstalled
		plugins[i].DiscoveryType = common.DiscoveryTypeLocal
	}

	return plugins, nil
}

// Clean deletes all plugins and tests.
func Clean() error {
	if err := catalog.CleanCatalogCache(); err != nil {
		return errors.Errorf("Failed to clean the catalog cache %v", err)
	}
	return os.RemoveAll(common.DefaultPluginRoot)
}

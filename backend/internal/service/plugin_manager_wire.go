package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// ProvidePluginManager builds the PluginManager and injects the account
// directory (sensitive capability) at assembly time, mirroring upstream's
// hand-maintained wire_gen.go statement. Keeping it in a provider makes the
// wiring survive `wire` regeneration.
func ProvidePluginManager(
	repo PluginRepository,
	encryptor SecretEncryptor,
	cfg *config.Config,
	hostInfo PluginHostInfo,
	kvStore PluginKVStore,
	accountDirectory PluginAccountDirectory,
) *PluginManager {
	pm := NewPluginManager(repo, encryptor, cfg, hostInfo, kvStore)
	pm.SetAccountDirectory(accountDirectory)
	return pm
}

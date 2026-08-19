# The plugin block's label must match the plugin BINARY's filename in
# plugin_dir ("nomad-driver-steamcmd" -- see `make build`/`make
# release-build`), NOT the driver's self-reported PluginInfo.Name
# ("steamcmd", used elsewhere e.g. `driver = "steamcmd"` in job specs).
# Nomad only loads a plugin_dir binary if a config block's label matches
# its filename -- get this wrong and the plugin is silently skipped
# entirely (absent from Nomad's own "detected plugin" log), not loaded
# with the wrong config.
plugin "nomad-driver-steamcmd" {
  config {
    steamcmd_path            = "steamcmd"
    max_concurrent_installs  = 2
    default_login_anonymous  = "true"
  }
}

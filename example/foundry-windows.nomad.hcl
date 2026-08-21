job "foundry" {
  datacenters = ["dc1"]

  group "foundry" {
    network {
      port "game" {
        static = 3724 # FOUNDRY's default port
      }
    }

    task "server" {
      driver = "steamcmd"

      config {
        app_id          = "2915550" # FOUNDRY: Dedicated Server
        platform        = "windows" # no native Linux build exists for this app --
                                     # without this, install_dir ends up containing
                                     # only Steam's own runtime scaffolding and no
                                     # actual server binary, with no error raised.
        windows_display = true      # this app needs a real X display even with
                                     # -batchmode -nographics -- confirmed directly,
                                     # that flag alone still fails at "Failed to
                                     # create batch mode window". Not every
                                     # Windows app needs this; it's opt-in per task.
        wine_prefix     = "/root/.wine-test" # whatever prefix you've verified
                                              # works on this client node -- see
                                              # the wine-stable warning in the README.
                                              # Could instead be set once fleet-wide
                                              # via default_wine_prefix in the
                                              # plugin's agent config if every
                                              # Windows task on this client shares
                                              # one prefix -- kept here per-task
                                              # since agent-config forwarding isn't
                                              # reliable on every Nomad version (see
                                              # PluginConfig's doc comment).

        launch {
          # Auto-wrapped by the driver: platform="windows" (above) means
          # the command below runs through `wine` automatically, and
          # windows_display=true additionally wraps that with
          # `xvfb-run --auto-servernum`, since this specific app (Unity-
          # based) needs a display even with -batchmode -nographics --
          # confirmed directly, that flag alone was NOT sufficient (it
          # still failed at "Failed to create batch mode window" without
          # a real X server present). See buildLaunchCommand in
          # driver/lifecycle.go for exactly what this expands to.
          command = "FoundryDedicatedServer.exe"
          args    = ["-batchmode", "-nographics"]
        }
      }

      resources {
        cpu    = 4000
        memory = 4096 # FOUNDRY's own docs recommend ~4GB
      }

      kill_timeout = "60s"
    }
  }
}

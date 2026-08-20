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
        app_id   = "2915550" # FOUNDRY: Dedicated Server
        platform = "windows" # no native Linux build exists for this app --
                              # without this, install_dir ends up containing
                              # only Steam's own runtime scaffolding and no
                              # actual server binary, with no error raised.

        launch {
          command = "xvfb-run"
          args    = ["wine", "local/steamapp/FoundryDedicatedServer.exe"]
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

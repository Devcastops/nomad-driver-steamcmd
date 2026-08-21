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
          # NOT "local/steamapp/FoundryDedicatedServer.exe" -- the launched
          # process's working directory is already install_dir (which IS
          # local/steamapp), so a bare filename here resolves correctly.
          # Confirmed the hard way: the doubled-path version fails with
          # `wine: failed to open "local/steamapp/FoundryDedicatedServer.exe"`
          # because Wine resolves it relative to a cwd that's already
          # local/steamapp, producing local/steamapp/local/steamapp/....
          args = ["wine", "FoundryDedicatedServer.exe"]
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

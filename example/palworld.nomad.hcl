job "palworld" {
  datacenters = ["dc1"]

  group "palworld" {
    volume "palworld-data" {
      type      = "csi"
      source    = "palworld-nfs"
      read_only = false
    }

    task "server" {
      driver = "steamcmd"

      volume_mount {
        volume      = "palworld-data"
        destination = "/data"
      }

      # Only needed if this task should use a non-anonymous account that
      # differs from the client's plugin-level default_login. Sourced via
      # Vault here, but the driver doesn't care -- Nomad variables or a
      # literal string work identically.
      template {
        data        = <<EOT
        {{ with secret "secret/data/steam/palworld" }}{{ .Data.data.password }}{{ end }}
        EOT
        destination = "local/steam_password"
        perms       = "0600"
      }

      config {
        app_id          = "2394010" # Palworld Dedicated Server
        install_dir     = "local/steamapp"
        update_on_start = true
        install_timeout = "20m"

        login_anonymous     = false
        login_username      = "your-steam-account"
        login_password_file = "local/steam_password"

        launch {
          command = "local/steamapp/PalServer.sh"
          args    = ["-port=8211", "-players=16", "-publiclobby"]
          env = {
            SAVE_DIR = "/data"
          }
        }
      }

      resources {
        cpu    = 4000
        memory = 8192
      }

      kill_timeout = "60s" # give the server time to flush saves on SIGTERM
    }
  }
}

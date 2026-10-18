job "foundry-proton" {
  datacenters = ["dc1"]

  group "foundry" {
    network {
      port "game" {
        static = 3724
      }
    }

    # Shared across the three tasks below (all live in the same allocation).
    # Absolute paths are required for sharing: relative paths resolve against
    # each task's own directory.
    #   install: ${NOMAD_ALLOC_DIR}/data/steamapp
    #   prefix : ${NOMAD_ALLOC_DIR}/data/compat

    # 1. Download/update the game. No `launch` block == install-only.
    task "install" {
      driver = "steamcmd"

      lifecycle {
        hook    = "prestart"
        sidecar = false
      }

      config {
        app_id          = "2915550"
        platform        = "windows"
        install_dir     = "${NOMAD_ALLOC_DIR}/data/steamapp"
        install_timeout = "30m"
      }

      resources {
        cpu    = 1000
        memory = 1024
      }
    }

    # 2. Create the Proton prefix once, before the server starts.
    #    steamcmd is skipped: install_dir is already populated and
    #    update_on_start defaults to false.
    task "prefix" {
      driver = "steamcmd"

      lifecycle {
        hook    = "prestart"
        sidecar = false
      }

      config {
        app_id           = "2915550"
        platform         = "windows"
        install_dir      = "${NOMAD_ALLOC_DIR}/data/steamapp"
        runner           = "proton"
        proton_path      = "/opt/GE-Proton9-20"
        compat_data_path = "${NOMAD_ALLOC_DIR}/data/compat"

        launch {
          command = "wineboot"
          args    = ["--init"]
        }
      }

      resources {
        cpu    = 1000
        memory = 1024
      }
    }

    # 3. Run the server only.
    task "server" {
      driver = "steamcmd"

      config {
        app_id           = "2915550"
        platform         = "windows"
        install_dir      = "${NOMAD_ALLOC_DIR}/data/steamapp"
        runner           = "proton"
        proton_path      = "/opt/GE-Proton9-20"
        compat_data_path = "${NOMAD_ALLOC_DIR}/data/compat"
        windows_display  = true # Unity app; still needs a display under Proton

        launch {
          command = "FoundryDedicatedServer.exe"
          args    = ["-batchmode", "-nographics"]
        }
      }

      resources {
        cpu    = 4000
        memory = 4096
      }

      kill_timeout = "60s"
    }
  }
}

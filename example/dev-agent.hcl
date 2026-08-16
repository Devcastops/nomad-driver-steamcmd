plugin "steamcmd" {
  config {
    steamcmd_path           = "steamcmd"
    max_concurrent_installs = 2

    default_login {
      anonymous = true
    }
  }
}

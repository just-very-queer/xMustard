use std::fs;

/// Application configuration.
pub struct Config {
    pub listen: String,
}

/// Load the configuration file from disk.
pub fn load_config(path: &str) -> Config {
    let listen = fs::read_to_string(path).unwrap_or_default();
    Config { listen }
}

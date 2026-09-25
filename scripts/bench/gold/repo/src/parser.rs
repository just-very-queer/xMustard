use std::collections::HashMap;

/// Parse a URL query string such as `a=1&b=2` into key/value pairs.
pub fn parse_query_string(input: &str) -> HashMap<String, String> {
    input
        .split('&')
        .filter_map(|pair| pair.split_once('='))
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

//! Server identity of the same configurations consumed by the compiler.
use serde_json::{Value, json};

/// Display metadata is not server identity, nor are the top-level profile keys the compiler
/// drops (a provider "meta" may change on every fetch). Everything below the profile root
/// remains significant (including group names, credentials and bind addresses).
pub fn identity(mut config: Value) -> Value {
    if let Some(root) = config.as_object_mut() {
        root.remove("name");
        root.remove("remarks");
        // Only a profile: a bare outbound's own fields sit at its root.
        if root.contains_key("outbounds") {
            root.retain(|key, _| !crate::profile::ignored_top_level_key(key));
        }
    }
    config
}

pub fn node_identity(config: Value) -> Value {
    let mut config = identity(config);
    if config.get("protocol").is_some() {
        config = json!({"outbounds": [config]});
    }
    if let Some(groups) = config.get_mut("elarniGroups").and_then(Value::as_array_mut) {
        for group in groups {
            if let Some(group) = group.as_object_mut() {
                group.remove("selected");
            }
        }
    }
    if config.as_object().is_some_and(|root| root.len() == 1)
        && let Some(members) = config.get_mut("outbounds").and_then(Value::as_array_mut)
        && members.len() == 1
        && let Some(member) = members[0].as_object_mut()
    {
        // A wrapped bare outbound keeps its display metadata on the member.
        for field in ["tag", "name", "remarks"] {
            member.remove(field);
        }
    }
    config
}

/// The REALITY values a panel such as 3x-ui picks anew on each fetch.
const ROTATED_REALITY_FIELDS: [&str; 3] = ["shortId", "serverName", "spiderX"];

/// [`node_identity`] without the REALITY values a panel rotates per fetch, wherever a stream's
/// REALITY settings appear. A refresh that changed them together with the remark matches the
/// server by this identity; address, port, credentials, public key and transport still count.
pub fn stable_node_identity(config: Value) -> Value {
    let mut config = node_identity(config);
    forget_rotated_reality(&mut config);
    config
}

fn forget_rotated_reality(value: &mut Value) {
    match value {
        Value::Object(object) => {
            if let Some(Value::Object(reality)) = object.get_mut("realitySettings") {
                for field in ROTATED_REALITY_FIELDS {
                    reality.remove(field);
                }
            }
            object.values_mut().for_each(forget_rotated_reality);
        }
        Value::Array(items) => items.iter_mut().for_each(forget_rotated_reality),
        _ => {}
    }
}

/// A stored REALITY shortId or serverName outlives a fetch that rotates it only this long after
/// the node's last good connect: a value the panel retired then fails every connect, and the
/// first refresh past the window takes the fetched one.
pub const KEEP_ROTATED_SECONDS: f64 = 86400.0;

/// Panels such as 3x-ui send one of a REALITY server's valid shortIds and serverNames at random
/// on each fetch. A refreshed config that differs from the stored one only there (display
/// metadata aside) keeps the stored values, so the refresh leaves the running tunnel alone,
/// while the node connected within `KEEP_ROTATED_SECONDS`; otherwise `config` returns as it came.
pub fn keep_short_ids(
    previous: &Value,
    config: Value,
    last_success_at: Option<f64>,
    now: f64,
) -> Value {
    if !last_success_at.is_some_and(|at| now - at < KEEP_ROTATED_SECONDS) {
        return config;
    }
    let mut kept = config.clone();
    adopt_short_ids(&mut kept, previous);
    if identity(kept.clone()) == identity(previous.clone()) {
        kept
    } else {
        config
    }
}

fn adopt_short_ids(config: &mut Value, previous: &Value) {
    match (config, previous) {
        (Value::Object(config), Value::Object(previous)) => {
            for (key, value) in config.iter_mut() {
                if let Some(old) = previous.get(key) {
                    adopt_short_ids(value, old);
                }
            }
            if let (Some(Value::Object(reality)), Some(Value::Object(old))) = (
                config.get_mut("realitySettings"),
                previous.get("realitySettings"),
            ) {
                for field in ["shortId", "serverName"] {
                    match old.get(field) {
                        Some(value) => reality.insert(field.into(), value.clone()),
                        None => reality.remove(field),
                    };
                }
            }
        }
        (Value::Array(config), Value::Array(previous)) => {
            for (value, old) in config.iter_mut().zip(previous) {
                adopt_short_ids(value, old);
            }
        }
        _ => {}
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_refresh_that_only_rotates_the_reality_short_id_keeps_the_stored_one() {
        let keep = |previous: &Value, config: Value| {
            keep_short_ids(previous, config, Some(1_000.0), 1_060.0)
        };
        // As a 3x-ui JSON subscription sends it.
        let server = |short_id: &str| {
            json!({"remarks":"NL","outbounds":[
                {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"nl.example.com","port":443,
                    "users":[{"id":"00000000-0000-4000-8000-000000000001","encryption":"none"}]}]},
                 "streamSettings":{"network":"tcp","security":"reality","realitySettings":{
                    "serverName":"example.com","publicKey":"key-1","shortId":short_id,"fingerprint":"chrome"}}},
                {"tag":"direct","protocol":"freedom"}]})
        };
        let stored = server("0a1b");
        assert_eq!(keep(&stored, server("c2d3")), stored);
        // Display metadata follows the provider; the shortId stays.
        let mut renamed = server("c2d3");
        renamed["remarks"] = json!("NL 2 GB left");
        let mut expected = stored.clone();
        expected["remarks"] = json!("NL 2 GB left");
        assert_eq!(keep(&stored, renamed), expected);
        let mut bare = server("");
        bare["outbounds"][0]["streamSettings"]["realitySettings"]
            .as_object_mut()
            .unwrap()
            .remove("shortId");
        assert_eq!(keep(&bare, server("c2d3")), bare);
        // 3x-ui also picks one of the server's serverNames at random.
        let mut sni = server("c2d3");
        sni["outbounds"][0]["streamSettings"]["realitySettings"]["serverName"] =
            json!("www.example.com");
        assert_eq!(keep(&stored, sni.clone()), stored);
        // A node that hasn't connected within the window takes the fetched values, so a
        // shortId the panel retired doesn't stay forever.
        assert_eq!(keep_short_ids(&stored, sni.clone(), None, 1_060.0), sni);
        assert_eq!(
            keep_short_ids(
                &stored,
                sni.clone(),
                Some(1_000.0),
                1_000.0 + KEEP_ROTATED_SECONDS
            ),
            sni
        );
        // Any other change takes the new values, shortId included.
        for (pointer, value) in [
            (
                "/outbounds/0/settings/vnext/0/address",
                json!("de.example.com"),
            ),
            ("/outbounds/0/settings/vnext/0/port", json!(8443)),
            (
                "/outbounds/0/settings/vnext/0/users/0/id",
                json!("00000000-0000-4000-8000-000000000002"),
            ),
            (
                "/outbounds/0/streamSettings/realitySettings/publicKey",
                json!("key-2"),
            ),
            (
                "/outbounds/0/streamSettings/realitySettings/fingerprint",
                json!("firefox"),
            ),
        ] {
            let mut changed = server("c2d3");
            *changed.pointer_mut(pointer).unwrap() = value;
            assert_eq!(keep(&stored, changed.clone()), changed, "{pointer}");
        }
    }

    // 3x-ui picks a REALITY server's shortId and serverName at random per fetch and puts the
    // traffic left into the remark: the stable identity still names the same server.
    #[test]
    fn stable_identity_leaves_out_only_the_reality_values_a_panel_rotates() {
        let server = |short_id: &str, server_name: &str, spider_x: &str| {
            json!({"remarks":"DE-1-9.52GB","outbounds":[
                {"tag":"proxy","protocol":"vless","settings":{"vnext":[{"address":"de.example.com","port":443,
                    "users":[{"id":"00000000-0000-4000-8000-000000000001","encryption":"none","flow":"xtls-rprx-vision"}]}]},
                 "streamSettings":{"network":"tcp","security":"reality","realitySettings":{
                    "serverName":server_name,"publicKey":"key-1","shortId":short_id,"spiderX":spider_x,
                    "fingerprint":"chrome"}}},
                {"tag":"direct","protocol":"freedom"}]})
        };
        let stored = server("0a1b", "www.example.com", "/");
        let mut rotated = server("c2d3", "cdn.example.org", "/a");
        rotated["remarks"] = json!("DE-1-9.47GB");
        assert_eq!(
            stable_node_identity(stored.clone()),
            stable_node_identity(rotated.clone())
        );
        // The exact identity still tells them apart.
        assert_ne!(node_identity(stored.clone()), node_identity(rotated));
        // A missing value is the same as a rotated one.
        let mut bare = stored.clone();
        bare["outbounds"][0]["streamSettings"]["realitySettings"] = json!({
            "publicKey":"key-1","fingerprint":"chrome"});
        assert_eq!(
            stable_node_identity(bare),
            stable_node_identity(stored.clone())
        );
        // Everything else still names another server.
        for (pointer, value) in [
            (
                "/outbounds/0/settings/vnext/0/address",
                json!("nl.example.com"),
            ),
            ("/outbounds/0/settings/vnext/0/port", json!(8443)),
            (
                "/outbounds/0/settings/vnext/0/users/0/id",
                json!("00000000-0000-4000-8000-000000000002"),
            ),
            ("/outbounds/0/settings/vnext/0/users/0/flow", json!("")),
            (
                "/outbounds/0/streamSettings/realitySettings/publicKey",
                json!("key-2"),
            ),
            (
                "/outbounds/0/streamSettings/realitySettings/fingerprint",
                json!("firefox"),
            ),
            ("/outbounds/0/streamSettings/network", json!("xhttp")),
            ("/outbounds/1/protocol", json!("blackhole")),
        ] {
            let mut changed = stored.clone();
            *changed.pointer_mut(pointer).unwrap() = value;
            assert_ne!(
                stable_node_identity(changed),
                stable_node_identity(stored.clone()),
                "{pointer}"
            );
        }
        // A field of that name elsewhere is not a REALITY value.
        let mut tls = stored.clone();
        tls["outbounds"][0]["streamSettings"] = json!({"security":"tls",
            "tlsSettings":{"serverName":"www.example.com"}});
        let mut other_tls = tls.clone();
        other_tls["outbounds"][0]["streamSettings"]["tlsSettings"]["serverName"] =
            json!("cdn.example.org");
        assert_ne!(stable_node_identity(tls), stable_node_identity(other_tls));
    }

    #[test]
    fn stable_identity_covers_bare_wrapped_and_nested_reality_settings() {
        let outbound = |short_id: &str, server_name: &str| {
            json!({"tag":"DE-1","protocol":"vless","settings":{"address":"de.example.com","port":443,
                "id":"00000000-0000-4000-8000-000000000001","encryption":"none"},
             "streamSettings":{"network":"xhttp","security":"reality","realitySettings":{
                "serverName":server_name,"publicKey":"key-1","shortId":short_id},
                "xhttpSettings":{"extra":{"downloadSettings":{"address":"dl.example.com","port":443,
                    "network":"xhttp","security":"reality","realitySettings":{
                        "serverName":server_name,"publicKey":"key-2","shortId":short_id}}}}}})
        };
        let bare = outbound("0a1b", "www.example.com");
        let wrapped =
            json!({"remarks":"Renamed","outbounds":[outbound("c2d3", "cdn.example.org")]});
        assert_eq!(
            stable_node_identity(bare.clone()),
            stable_node_identity(wrapped)
        );
        // The download side's own REALITY key still counts.
        let mut other_key = bare.clone();
        other_key["streamSettings"]["xhttpSettings"]["extra"]["downloadSettings"]["realitySettings"]
            ["publicKey"] = json!("key-3");
        assert_ne!(stable_node_identity(bare), stable_node_identity(other_key));
    }

    #[test]
    fn identity_preserves_nested_semantics_and_ignores_only_display_metadata() {
        assert_eq!(
            identity(json!({"name":"A","outbounds":[]})),
            identity(json!({"remarks":"B","outbounds":[]}))
        );
        assert_ne!(
            identity(json!({"groups":[{"name":"A"}]})),
            identity(json!({"groups":[{"name":"B"}]}))
        );
        assert_ne!(
            identity(json!({"sendThrough":"a"})),
            identity(json!({"sendThrough":"b"}))
        );
    }

    // compile_profile drops top-level keys Xray ignores, so a provider "meta" that changes on
    // every fetch must not make the same server look new.
    #[test]
    fn identity_ignores_top_level_keys_the_compiler_drops() {
        let server = |meta: &str, address: &str| {
            json!({"remarks":"NL","meta":{"updated":meta},"version":{"min":meta},"outbounds":[
                {"tag":"proxy","protocol":"vless","settings":{"address":address,"port":443,
                    "id":"00000000-0000-4000-8000-000000000001","encryption":"none"}}]})
        };
        assert_eq!(
            identity(server("1", "a.example.com")),
            identity(server("2", "a.example.com"))
        );
        assert_eq!(
            node_identity(server("1", "a.example.com")),
            node_identity(server("2", "a.example.com"))
        );
        assert_ne!(
            identity(server("1", "a.example.com")),
            identity(server("1", "b.example.com"))
        );
        // Group choices and keys Xray reads still count.
        for (key, value) in [
            ("elarniGroups", json!([{"name":"PROXY"}])),
            ("policy", json!({"levels":{}})),
            ("Policy", json!({})),
        ] {
            let mut changed = server("1", "a.example.com");
            changed[key] = value;
            assert_ne!(
                identity(changed),
                identity(server("1", "a.example.com")),
                "{key}"
            );
        }
    }

    #[test]
    fn stable_matching_tolerates_single_outbound_wrapping_and_preserves_binding() {
        let outbound = json!({"tag":"display","protocol":"vless","settings":{"id":"secret"}});
        assert_eq!(
            node_identity(outbound.clone()),
            node_identity(json!({"remarks":"Renamed","outbounds":[outbound.clone()]}))
        );
        let mut rebound = outbound.clone();
        rebound["sendThrough"] = json!("192.0.2.1");
        assert_ne!(node_identity(outbound), node_identity(rebound));
    }

    // The importer wraps a bare outbound and keeps its remarks as the name.
    #[test]
    fn renaming_a_wrapped_single_outbound_keeps_its_identity() {
        let named =
            |name: &str| json!({"remarks":name,"protocol":"vless","settings":{"id":"secret"}});
        assert_eq!(
            node_identity(json!({"outbounds":[named("Server A")]})),
            node_identity(json!({"outbounds":[named("Server B")]}))
        );
        assert_eq!(
            node_identity(named("Server A")),
            node_identity(json!({"outbounds":[named("Server A")]}))
        );
    }
}

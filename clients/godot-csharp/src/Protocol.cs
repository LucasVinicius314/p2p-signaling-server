using System;
using System.Collections.Generic;
using System.Linq;
using System.Text.Json;
using Godot;

namespace P2PSignaling;

/// <summary>A message from the server (see docs/protocol.md).</summary>
internal readonly struct ServerMessage
{
    private readonly JsonElement _root;

    private ServerMessage(JsonElement root) => _root = root;

    public string Type => Str("t") ?? "";

    public static bool TryParse(string json, out ServerMessage msg)
    {
        msg = default;
        try
        {
            var doc = JsonDocument.Parse(json);
            if (doc.RootElement.ValueKind != JsonValueKind.Object) return false;
            msg = new ServerMessage(doc.RootElement.Clone());
            return true;
        }
        catch (JsonException)
        {
            return false;
        }
    }

    public string? Str(string name) =>
        _root.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.String ? v.GetString() : null;

    public int Int(string name, int fallback = 0) =>
        _root.TryGetProperty(name, out var v) && v.ValueKind == JsonValueKind.Number && v.TryGetInt32(out var i) ? i : fallback;

    public JsonElement? Raw(string name) =>
        _root.TryGetProperty(name, out var v) ? v : null;

    public int[] IntArray(string name)
    {
        if (!_root.TryGetProperty(name, out var v) || v.ValueKind != JsonValueKind.Array) return Array.Empty<int>();
        return v.EnumerateArray().Where(e => e.ValueKind == JsonValueKind.Number).Select(e => e.GetInt32()).ToArray();
    }
}

/// <summary>Builds the JSON the server expects.</summary>
internal static class Outgoing
{
    private static string Json(Dictionary<string, object?> m) => JsonSerializer.Serialize(m);

    public static string Hello(string bucket, string gameVersion, string? name)
    {
        var m = new Dictionary<string, object?>
        {
            ["t"] = "hello", ["bucket"] = bucket, ["game_version"] = gameVersion, ["protocol"] = 1,
        };
        if (!string.IsNullOrEmpty(name)) m["name"] = name;
        return Json(m);
    }

    public static string CreateRoom(int maxPlayers)
    {
        var m = new Dictionary<string, object?> { ["t"] = "create_room" };
        if (maxPlayers > 0) m["max_players"] = maxPlayers;
        return Json(m);
    }

    public static string JoinRoom(string code) => Json(new() { ["t"] = "join_room", ["code"] = code });
    public static string Approve(int peer, bool accept) => Json(new() { ["t"] = "approve", ["peer"] = peer, ["accept"] = accept });
    public static string Lock(bool locked) => Json(new() { ["t"] = "lock_room", ["locked"] = locked });
    public static string Leave() => Json(new() { ["t"] = "leave" });
    public static string Ping() => Json(new() { ["t"] = "ping" });

    // Signal payloads are relayed untouched by the server; this is the format this library uses.
    public static string Description(int to, string kind, string sdp) =>
        Json(new() { ["t"] = "signal", ["to"] = to, ["kind"] = kind, ["data"] = new Dictionary<string, object?> { ["sdp"] = sdp } });

    public static string Candidate(int to, string media, long index, string name) =>
        Json(new()
        {
            ["t"] = "signal", ["to"] = to, ["kind"] = "candidate",
            ["data"] = new Dictionary<string, object?> { ["media"] = media, ["index"] = index, ["name"] = name },
        });
}

internal static class IceConfig
{
    /// <summary>Turns the server's welcome message into a WebRTCPeerConnection configuration.</summary>
    public static Godot.Collections.Dictionary Build(ServerMessage welcome)
    {
        var servers = new Godot.Collections.Array();
        if (welcome.Raw("ice_servers") is { ValueKind: JsonValueKind.Array } arr)
        {
            foreach (var s in arr.EnumerateArray())
            {
                var entry = new Godot.Collections.Dictionary();
                var urls = new Godot.Collections.Array();
                if (s.TryGetProperty("urls", out var u) && u.ValueKind == JsonValueKind.Array)
                    foreach (var url in u.EnumerateArray()) urls.Add(url.GetString() ?? "");
                entry["urls"] = urls;
                if (s.TryGetProperty("username", out var un) && un.ValueKind == JsonValueKind.String) entry["username"] = un.GetString() ?? "";
                if (s.TryGetProperty("credential", out var c) && c.ValueKind == JsonValueKind.String) entry["credential"] = c.GetString() ?? "";
                servers.Add(entry);
            }
        }
        var config = new Godot.Collections.Dictionary { ["iceServers"] = servers };
        if (welcome.Str("ice_policy") == "relay") config["iceTransportPolicy"] = "relay";
        return config;
    }
}

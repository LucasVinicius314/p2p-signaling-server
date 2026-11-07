using Godot;

namespace P2PSignaling;

/// <summary>
/// Godot 4 ships WebRTC as a native extension, so WebRTCPeerConnection and WebRTCMultiplayerPeer
/// have no C# bindings. They exist in ClassDB though, so they are driven by name here.
/// </summary>
internal static class Rtc
{
    public static MultiplayerPeer? NewMultiplayerPeer() =>
        ClassDB.ClassExists("WebRTCMultiplayerPeer") && ClassDB.Instantiate("WebRTCMultiplayerPeer").AsGodotObject() is MultiplayerPeer p ? p : null;

    public static GodotObject? NewConnection() =>
        ClassDB.ClassExists("WebRTCPeerConnection") ? ClassDB.Instantiate("WebRTCPeerConnection").AsGodotObject() : null;

    private static Error Err(Variant v) => (Error)v.AsInt64();

    public static Error CreateServer(MultiplayerPeer p) => Err(p.Call("create_server"));
    public static Error CreateClient(MultiplayerPeer p, int id) => Err(p.Call("create_client", id));
    public static Error CreateMesh(MultiplayerPeer p, int id) => Err(p.Call("create_mesh", id));
    public static Error AddPeer(MultiplayerPeer p, GodotObject conn, int id) => Err(p.Call("add_peer", conn, id));
    public static void RemovePeer(MultiplayerPeer p, int id) => p.Call("remove_peer", id);

    public static Error Initialize(GodotObject c, Godot.Collections.Dictionary config) => Err(c.Call("initialize", config));
    public static Error CreateOffer(GodotObject c) => Err(c.Call("create_offer"));
    public static Error SetLocalDescription(GodotObject c, string type, string sdp) => Err(c.Call("set_local_description", type, sdp));
    public static Error SetRemoteDescription(GodotObject c, string type, string sdp) => Err(c.Call("set_remote_description", type, sdp));
    public static Error AddIceCandidate(GodotObject c, string media, long index, string name) => Err(c.Call("add_ice_candidate", media, index, name));
    public static void Close(GodotObject c) => c.Call("close");
}

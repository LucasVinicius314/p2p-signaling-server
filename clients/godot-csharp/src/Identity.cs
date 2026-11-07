using System;
using System.Collections.Generic;
using System.IO;
using System.Security.Cryptography;
using System.Text;

namespace P2PSignaling;

/// <summary>One person in the friends file: someone whose rooms you want to see.</summary>
public sealed record Friend(string Id, string Label, string Tag);

/// <summary>What <see cref="P2PIdentity.LoadFriends"/> found.</summary>
public sealed record FriendList(IReadOnlyList<Friend> Friends, IReadOnlyList<string> Problems);

/// <summary>
/// This machine's identity for the p2p-signaling service, shared by every game that uses the library, and the list of
/// friends whose rooms this player wants to see. Both live in a folder in the user's home directory:
/// <code>
/// ~/.p2p-signaling-server/id        this machine's id, created on first use
/// ~/.p2p-signaling-server/friends   one friend's id per line; blank lines and # comments are ignored
/// </code>
/// A player gives their id to friends; a room hosted with <see cref="P2PLobby.ListForFriends"/> is then listed to everyone
/// who has that id in their friends file (<see cref="P2PLobby.ListFriendRooms"/>). The id itself never goes to the
/// server, only a hash of it (the <em>tag</em>), but the id is what makes a room visible, so it should be shared with
/// friends only. Environment variable <c>P2P_SIGNALING_HOME</c> replaces the folder (for tests and for running two
/// players on one machine).
/// </summary>
public static class P2PIdentity
{
    private const string Alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"; // Crockford base32: no I, L, O, U
    private const int DataBytes = 16;
    private const int DataChars = 26; // 128 bits in 5-bit symbols
    private const int CheckChars = 2;
    private const string TagPrefix = "p2p-signaling-server/friends/v1:";

    /// <summary>The most friends one list request carries (matches the server's limit).</summary>
    public const int MaxFriends = 100;

    private static readonly object Lock = new();
    private static string? _ownId;
    private static string? _ownProblem;
    private static bool _persistent;

    /// <summary>The folder that holds <c>id</c> and <c>friends</c>.</summary>
    public static string HomeFolder
    {
        get
        {
            var overridden = Environment.GetEnvironmentVariable("P2P_SIGNALING_HOME");
            if (!string.IsNullOrWhiteSpace(overridden)) return overridden;
            var home = Environment.GetFolderPath(Environment.SpecialFolder.UserProfile);
            return Path.Combine(string.IsNullOrEmpty(home) ? Path.GetTempPath() : home, ".p2p-signaling-server");
        }
    }

    /// <summary>
    /// This machine's id in the form to share, for example <c>7H3K-9QWE-...</c> (seven groups of four). Created and saved
    /// the first time it is asked for; every game on the machine gets the same one.
    /// </summary>
    public static string OwnId => Format(Canonical());

    /// <summary>The hash of <see cref="OwnId"/> that rooms are tagged with. Safe to send to the server.</summary>
    public static string OwnTag => TagOf(Canonical());

    /// <summary>False when the id could not be saved or read, so it only lasts until the game closes. See <see cref="OwnProblem"/>.</summary>
    public static bool IsPersistent { get { Canonical(); return _persistent; } }

    /// <summary>Why the id is not persistent (read-only home folder, a damaged file), or null.</summary>
    public static string? OwnProblem { get { Canonical(); return _ownProblem; } }

    /// <summary>Forgets what was loaded, so the next use reads the files again (tests, or after the folder changed).</summary>
    public static void Reset()
    {
        lock (Lock) { _ownId = null; _ownProblem = null; _persistent = false; }
    }

    // ---- ids ----

    /// <summary>Makes a new random id (not saved anywhere).</summary>
    public static string Generate()
    {
        var data = new byte[DataBytes];
        RandomNumberGenerator.Fill(data);
        return Encode(data);
    }

    /// <summary>Accepts an id as people write it: any case, with or without dashes or spaces, O read as 0 and I or L as 1.</summary>
    public static bool TryParseId(string? text, out string canonical)
    {
        canonical = "";
        if (text == null) return false;
        var sb = new StringBuilder(DataChars + CheckChars);
        foreach (var raw in text)
        {
            if (raw is '-' or ' ' or '_') continue;
            var c = char.ToUpperInvariant(raw);
            c = c switch { 'O' => '0', 'I' or 'L' => '1', _ => c };
            if (Alphabet.IndexOf(c) < 0) return false;
            sb.Append(c);
        }
        if (sb.Length != DataChars + CheckChars) return false;
        var s = sb.ToString();
        if (!TryDecode(s[..DataChars], out var data)) return false;
        if (s[DataChars..] != Check(data)) return false; // a typo
        canonical = s;
        return true;
    }

    /// <summary>The id in groups of four, easy to read out and to paste.</summary>
    public static string Format(string canonical)
    {
        var sb = new StringBuilder();
        for (int i = 0; i < canonical.Length; i += 4)
        {
            if (i > 0) sb.Append('-');
            sb.Append(canonical, i, Math.Min(4, canonical.Length - i));
        }
        return sb.ToString();
    }

    /// <summary>The tag of an id: lowercase hex of a SHA-256. A room's host tag and the tags in a list request are these.</summary>
    public static string TagOf(string canonical)
    {
        var hash = SHA256.HashData(Encoding.UTF8.GetBytes(TagPrefix + canonical));
        return Convert.ToHexString(hash).ToLowerInvariant();
    }

    private static string Encode(byte[] data)
    {
        var sb = new StringBuilder(DataChars + CheckChars);
        int buffer = 0, bits = 0;
        foreach (var b in data)
        {
            buffer = (buffer << 8) | b;
            bits += 8;
            while (bits >= 5)
            {
                sb.Append(Alphabet[(buffer >> (bits - 5)) & 31]);
                bits -= 5;
            }
        }
        if (bits > 0) sb.Append(Alphabet[(buffer << (5 - bits)) & 31]);
        sb.Append(Check(data));
        return sb.ToString();
    }

    private static bool TryDecode(string chars, out byte[] data)
    {
        data = new byte[DataBytes];
        int buffer = 0, bits = 0, n = 0;
        foreach (var c in chars)
        {
            buffer = (buffer << 5) | Alphabet.IndexOf(c);
            bits += 5;
            if (bits >= 8)
            {
                if (n == DataBytes) return false;
                data[n++] = (byte)((buffer >> (bits - 8)) & 0xFF);
                bits -= 8;
            }
        }
        return n == DataBytes && (buffer & ((1 << bits) - 1)) == 0; // the two spare bits must be zero
    }

    private static string Check(byte[] data)
    {
        var h = SHA256.HashData(data);
        int ten = (h[0] << 2) | (h[1] >> 6); // first 10 bits
        return new string(new[] { Alphabet[(ten >> 5) & 31], Alphabet[ten & 31] });
    }

    // ---- this machine's id ----

    private static string Canonical()
    {
        lock (Lock)
        {
            if (_ownId != null) return _ownId;
            try
            {
                var path = Path.Combine(HomeFolder, "id");
                if (File.Exists(path))
                {
                    if (TryParseId(FirstLine(File.ReadAllLines(path)), out var existing))
                    {
                        _ownId = existing;
                        _persistent = true;
                        return existing;
                    }
                    // Never overwrite a file we cannot read: it may be the player's real id, damaged by a stray edit.
                    _ownProblem = $"{path} does not hold a valid id; delete it to get a new one. Using a temporary id.";
                }
                else
                {
                    _ownId = CreateFile(path);
                    _persistent = true;
                    return _ownId;
                }
            }
            catch (Exception e) when (e is IOException or UnauthorizedAccessException or NotSupportedException)
            {
                _ownProblem = $"could not save the id ({e.Message}). Using a temporary id.";
            }
            _ownId = GenerateCanonical();
            return _ownId;
        }
    }

    private static string GenerateCanonical()
    {
        TryParseId(Generate(), out var canonical);
        return canonical;
    }

    private static string FirstLine(string[] lines)
    {
        foreach (var raw in lines)
        {
            var line = raw.Trim();
            if (line.Length > 0 && !line.StartsWith('#')) return line;
        }
        return "";
    }

    /// <summary>Writes a new id file without ever replacing one: the file is made in full under a temporary name, then moved into place, so two games starting together end up with the same id.</summary>
    private static string CreateFile(string path)
    {
        var folder = Path.GetDirectoryName(path)!;
        Directory.CreateDirectory(folder);
        var canonical = GenerateCanonical();
        var temp = Path.Combine(folder, $"id.{Guid.NewGuid():N}.tmp");
        File.WriteAllText(temp,
            "# Your p2p-signaling-server id, shared by every game that uses it.\n" +
            "# Give it to friends so they can see your rooms (they put it in their 'friends' file).\n" +
            "# Do not share it with strangers. Deleting this file makes a new id.\n" +
            Format(canonical) + "\n");
        try
        {
            if (!OperatingSystem.IsWindows())
                File.SetUnixFileMode(temp, UnixFileMode.UserRead | UnixFileMode.UserWrite);
            File.Move(temp, path, overwrite: false);
            return canonical;
        }
        catch (IOException) when (File.Exists(path))
        {
            // Another game created it first: use theirs.
            File.Delete(temp);
            if (TryParseId(FirstLine(File.ReadAllLines(path)), out var theirs)) return theirs;
            throw;
        }
    }

    // ---- friends ----

    /// <summary>
    /// Reads the friends file (and creates it, empty and explained, if it does not exist yet). Call it again whenever
    /// the list is needed; the player may have edited it in the meantime. Bad lines are skipped and described in
    /// <see cref="FriendList.Problems"/>.
    /// </summary>
    public static FriendList LoadFriends()
    {
        var friends = new List<Friend>();
        var problems = new List<string>();
        var path = Path.Combine(HomeFolder, "friends");
        string[] lines;
        try
        {
            if (!File.Exists(path))
            {
                Directory.CreateDirectory(HomeFolder);
                File.WriteAllText(path,
                    "# Friends whose rooms you want to see: one id per line, as they gave it to you.\n" +
                    "# Text after a # is just a note for you, for example:\n" +
                    "# 7H3K-9QWE-...-....   # Marina\n");
            }
            lines = File.ReadAllLines(path);
        }
        catch (Exception e) when (e is IOException or UnauthorizedAccessException or NotSupportedException)
        {
            problems.Add($"could not read {path} ({e.Message})");
            return new FriendList(friends, problems);
        }

        var seen = new HashSet<string>();
        string own = Canonical();
        for (int i = 0; i < lines.Length; i++)
        {
            var line = lines[i];
            string label = "";
            int hash = line.IndexOf('#');
            if (hash >= 0)
            {
                label = line[(hash + 1)..].Trim();
                line = line[..hash];
            }
            line = line.Trim();
            if (line.Length == 0) continue;
            if (!TryParseId(line, out var id))
            {
                problems.Add($"line {i + 1} of the friends file is not a valid id (check for typos)");
                continue;
            }
            if (id == own || !seen.Add(id)) continue; // yourself, or a repeat
            if (friends.Count == MaxFriends)
            {
                problems.Add($"the friends file has more than {MaxFriends} ids; the rest are ignored");
                break;
            }
            friends.Add(new Friend(Format(id), label, TagOf(id)));
        }
        return new FriendList(friends, problems);
    }
}

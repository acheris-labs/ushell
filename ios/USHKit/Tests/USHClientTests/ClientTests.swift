import CryptoKit
import Foundation
import XCTest
import USHCore
@testable import USHClient

/// Collects a session's output and waits for text in it.
final class Output: @unchecked Sendable {
    private let lock = NSLock()
    private var text = ""
    func append(_ b: [UInt8]) { lock.withLock { text += String(decoding: b, as: UTF8.self) } }
    func waitFor(_ needle: String, seconds: Double = 10) async throws {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end {
            if lock.withLock({ text.contains(needle) }) { return }
            try await Task.sleep(for: .milliseconds(50))
        }
        let seen = lock.withLock { text }
        XCTFail("never saw \(needle.debugDescription) in \(seen.debugDescription)")
        throw CancellationError()
    }
}

/// The join's exit code, or a failure instead of a hang: past the deadline
/// the channel is closed, which ends the wait with ClientError.disconnected.
func exitCode(of j: JoinChannel, within seconds: Double = 15) async throws -> Int {
    let deadline = Task {
        try await Task.sleep(for: .seconds(seconds))
        j.close()
    }
    defer { deadline.cancel() }
    return try await j.exitCode()
}

final class ClientTests: XCTestCase {
    var profile: Profile!

    override func setUpWithError() throws {
        let env = ProcessInfo.processInfo.environment
        guard let port = env["USH_TEST_PORT"].flatMap(Int.init), let seed = env["USH_TEST_SEED"] else {
            throw XCTSkip("needs a local ushd: make test-ios-client")
        }
        profile = Profile(name: "test", host: "127.0.0.1", port: port, user: "ush", seed: seed)
    }

    func session(_ name: String, on c: RendezvousConnection) async throws -> SessionInfo {
        let listed = try await c.list()
        return try XCTUnwrap(listed.first { $0.name == name }, "\(name) is not listed")
    }

    func testListShowsTheSharedSessions() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let names = try await c.list().map(\.name)
        XCTAssertTrue(Set(["swift-echo", "swift-other"]).isSubset(of: names), "\(names)")
    }

    func testJoinTypesAndTheListKeyExitsTen() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let out = Output()
        let j = try await c.join(sessionID: session("swift-echo", on: c).id, cols: 80, rows: 24) { out.append($0) }
        j.send(Array("hello\r".utf8))
        try await out.waitFor("got:hello")
        j.send([0x1d])
        try await out.waitFor("back to list")
        j.send(Array("l".utf8))
        let code = try await exitCode(of: j)
        XCTAssertEqual(code, 10)
    }

    func testTheDisconnectKeyExitsEleven() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let out = Output()
        let j = try await c.join(sessionID: session("swift-other", on: c).id, cols: 80, rows: 24) { out.append($0) }
        j.send([0x1d])
        try await out.waitFor("back to list")
        j.send(Array("d".utf8))
        let code = try await exitCode(of: j)
        XCTAssertEqual(code, 11)
    }

    func testASessionThatEndsExitsZero() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let out = Output()
        let j = try await c.join(sessionID: session("swift-ender", on: c).id, cols: 80, rows: 24) { out.append($0) }
        j.send(Array("quit\r".utf8))
        let code = try await exitCode(of: j)
        XCTAssertEqual(code, 0)
    }

    func testJoiningAGoneSessionIsRefused() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let j = try await c.join(sessionID: "zzzzzzzzzzzzzzzzzzzz", cols: 80, rows: 24) { _ in }
        let code = try await exitCode(of: j)
        XCTAssertNotEqual(code, 0)
        XCTAssertEqual(joinEnd(exitCode: code), .ended("could not join"))
    }

    func testResizeReachesTheSession() async throws {
        let c = try await RendezvousConnection.connect(profile)
        defer { c.close() }
        let j = try await c.join(sessionID: session("swift-other", on: c).id, cols: 80, rows: 24) { _ in }
        j.resize(cols: 100, rows: 30) // must not throw or kill the channel
        try await Task.sleep(for: .milliseconds(300))
        j.close()
    }

    func testAWrongHostKeyIsAHardStop() async throws {
        let real = try Keys(seed: parseSeed(profile.seed))
        let keys = Keys(login: real.login, hostPublicKey: Curve25519.Signing.PrivateKey().publicKey)
        do {
            _ = try await RendezvousConnection.connect(profile, keys: keys)
            XCTFail("connected to a rendezvous with the wrong host key")
        } catch let e as ClientError {
            XCTAssertEqual(e, .hostKeyMismatch)
        }
    }

    func testAWrongLoginKeyIsRefused() async throws {
        let real = try Keys(seed: parseSeed(profile.seed))
        let keys = Keys(login: Curve25519.Signing.PrivateKey(), hostPublicKey: real.hostPublicKey)
        do {
            _ = try await RendezvousConnection.connect(profile, keys: keys)
            XCTFail("logged in with the wrong key")
        } catch let e as ClientError {
            XCTAssertEqual(e, .authenticationRefused)
        }
    }

    func testNothingListeningIsUnreachable() async throws {
        var p = profile!
        p.port = 1
        do {
            _ = try await RendezvousConnection.connect(p)
            XCTFail("connected to port 1")
        } catch let e as ClientError {
            guard case .unreachable = e else { return XCTFail("\(e)") }
        }
    }

    func testListOnAClosedConnectionThrowsInsteadOfCrashing() async throws {
        let c = try await RendezvousConnection.connect(profile)
        c.close()
        try await c.channel.closeFuture.get()
        do {
            _ = try await c.list()
            XCTFail("listed over a closed connection")
        } catch {}
    }

    func testASilentConnectionTimesOutAndIsDropped() async throws {
        let proxy = try await FreezableProxy(target: profile.port)
        var p = profile!
        p.port = proxy.port
        let c = try await RendezvousConnection.connect(p)
        defer { c.close() }
        _ = try await c.list()
        proxy.freeze()
        let start = Date()
        do {
            _ = try await c.list(timeout: .seconds(2))
            XCTFail("list answered through a frozen link")
        } catch let e as ClientError {
            XCTAssertEqual(e, .timedOut)
        }
        XCTAssertLessThan(Date().timeIntervalSince(start), 5)
        XCTAssertFalse(c.isActive, "a timed-out connection must be dropped so the next use reconnects")
    }
}

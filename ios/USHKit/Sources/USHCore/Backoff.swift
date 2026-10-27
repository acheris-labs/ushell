/// Reconnect delays in seconds, as `ush share -k` uses: 2, 4, 8, 16, 30, 30…
public struct Backoff: Sendable {
    private var next: Double = 2
    public init() {}
    public mutating func delay() -> Double {
        defer { next = min(next * 2, 30) }
        return next
    }
    public mutating func reset() { next = 2 }
}

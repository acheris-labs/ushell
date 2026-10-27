import Foundation
import Security
import USHCore

/// Profiles in the Keychain, one item per rendezvous, readable only on this
/// device while it is unlocked. The seed is the key to every session.
struct ProfileStore {
    private let service = "net.acheris.ush.profiles"

    func load() -> [Profile] {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecMatchLimit as String: kSecMatchLimitAll,
            kSecReturnData as String: true,
        ]
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let items = result as? [Data] else { return [] }
        return items.compactMap { try? JSONDecoder().decode(Profile.self, from: $0) }
            .sorted { $0.name < $1.name }
    }

    /// Replaces the item in place when it exists, so a failure leaves the old
    /// one behind rather than nothing.
    func save(_ profile: Profile) throws {
        let data = try JSONEncoder().encode(profile)
        let item: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: profile.name,
        ]
        var status = SecItemUpdate(item as CFDictionary,
                                   [kSecValueData as String: data] as CFDictionary)
        if status == errSecItemNotFound {
            var add = item
            add[kSecAttrAccessible as String] = kSecAttrAccessibleWhenUnlockedThisDeviceOnly
            add[kSecValueData as String] = data
            status = SecItemAdd(add as CFDictionary, nil)
        }
        guard status == errSecSuccess else {
            throw NSError(domain: NSOSStatusErrorDomain, code: Int(status))
        }
    }

    func delete(name: String) {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: name,
        ]
        SecItemDelete(query as CFDictionary)
    }
}

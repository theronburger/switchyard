#!/usr/bin/env swift
// Verifies a Sparkle EdDSA item signature cryptographically.
//
// usage: verify-sparkle-signature.swift <archive> <base64-public-key> <base64-signature>
//
// Sparkle signs the raw archive bytes with Ed25519 and publishes the
// signature as `sparkle:edSignature`; the app verifies it against the
// embedded `SUPublicEDKey` before extraction. This script performs the same
// check so the release pipeline proves the appcast it is about to arm was
// signed with the key the shipped app trusts. Exit 0 only on a valid signature.

import CryptoKit
import Foundation

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data("\(message)\n".utf8))
    exit(1)
}

let arguments = CommandLine.arguments
guard arguments.count == 4 else {
    fail("usage: verify-sparkle-signature.swift <archive> <base64-public-key> <base64-signature>")
}

guard let archive = FileManager.default.contents(atPath: arguments[1]) else {
    fail("archive could not be read")
}
guard let publicKeyData = Data(base64Encoded: arguments[2].trimmingCharacters(in: .whitespacesAndNewlines)),
      publicKeyData.count == 32 else {
    fail("public key must be 32 base64-encoded bytes")
}
guard let signature = Data(base64Encoded: arguments[3].trimmingCharacters(in: .whitespacesAndNewlines)),
      signature.count == 64 else {
    fail("signature must be 64 base64-encoded bytes")
}
guard let publicKey = try? Curve25519.Signing.PublicKey(rawRepresentation: publicKeyData) else {
    fail("public key is not a valid Ed25519 key")
}
guard publicKey.isValidSignature(signature, for: archive) else {
    fail("Sparkle signature does not verify against the supplied public key")
}
print("sparkle signature verified")

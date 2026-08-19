#!/usr/bin/env swift

import CryptoKit
import Foundation

let encoded = String(data: FileHandle.standardInput.readDataToEndOfFile(), encoding: .utf8)?
    .trimmingCharacters(in: .whitespacesAndNewlines) ?? ""

guard let secret = Data(base64Encoded: encoded) else {
    FileHandle.standardError.write(Data("invalid base64 Sparkle private key\n".utf8))
    exit(1)
}

let publicKey: Data
switch secret.count {
case 32:
    do {
        publicKey = try Curve25519.Signing.PrivateKey(rawRepresentation: secret).publicKey.rawRepresentation
    } catch {
        FileHandle.standardError.write(Data("invalid Sparkle private seed\n".utf8))
        exit(1)
    }
case 96:
    publicKey = secret.suffix(32)
default:
    FileHandle.standardError.write(Data("unsupported Sparkle private key length\n".utf8))
    exit(1)
}

print(publicKey.base64EncodedString())

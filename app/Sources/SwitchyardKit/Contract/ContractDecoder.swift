import Foundation

public struct ContractDecoder: Sendable {
    public init() {}

    public func decode<Value: Decodable>(_ type: Value.Type, from data: Data) throws -> Value {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let value = try container.decode(String.self)
            guard let date = RFC3339Date.parse(value) else {
                throw DecodingError.dataCorruptedError(
                    in: container,
                    debugDescription: "Expected an RFC 3339 timestamp"
                )
            }
            return date
        }
        return try decoder.decode(type, from: data)
    }
}

private enum RFC3339Date {
    static func parse(_ value: String) -> Date? {
        guard let fractionRange = fractionalRange(in: value) else {
            return formatter().date(from: value)
        }

        let digits = value[fractionRange]
        guard digits.count <= 9, digits.allSatisfy(\.isNumber),
              let fraction = Double("0." + digits) else {
            return nil
        }
        var wholeSeconds = value
        let decimalPoint = wholeSeconds.index(before: fractionRange.lowerBound)
        wholeSeconds.removeSubrange(decimalPoint..<fractionRange.upperBound)
        guard let date = formatter().date(from: wholeSeconds) else {
            return nil
        }
        return date.addingTimeInterval(fraction)
    }

    private static func fractionalRange(in value: String) -> Range<String.Index>? {
        guard let timeSeparator = value.firstIndex(of: "T"),
              let decimalPoint = value[timeSeparator...].firstIndex(of: ".") else {
            return nil
        }
        let fractionStart = value.index(after: decimalPoint)
        guard let fractionEnd = value[fractionStart...].firstIndex(where: { character in
            character == "Z" || character == "+" || character == "-"
        }), fractionStart < fractionEnd else {
            return nil
        }
        return fractionStart..<fractionEnd
    }

    private static func formatter() -> ISO8601DateFormatter {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withColonSeparatorInTimeZone]
        return formatter
    }
}

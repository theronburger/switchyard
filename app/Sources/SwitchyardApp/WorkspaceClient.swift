import Foundation

protocol WorkspaceAPI: Sendable {
    func status() async throws -> WorkspaceSnapshot
    func run(_ request: RunRequest) async throws -> WorkspaceRun
    func stop(path: String) async throws -> WorkspaceRun
    func choices(path: String, choices: WorkspaceChoices) async throws
    func logs(path: String) async throws -> WorkspaceLog
    func configuration() async throws -> String
    func saveConfiguration(_ json: String) async throws
    func prunePlan(path: String) async throws -> PrunePlan
    func prune(path: String) async throws
    func create(_ request: CreateRequest) async throws
}

struct WorkspaceAPIError: LocalizedError, Sendable {
    let message: String
    var errorDescription: String? { message }
}

private final class LocalOnlyRedirects: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping @Sendable (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}

actor WorkspaceClient: WorkspaceAPI {
    let root: URL
    private let session: URLSession

    init(root: URL, session: URLSession? = nil) {
        self.root = root
        self.session = session ?? URLSession(configuration: .ephemeral, delegate: LocalOnlyRedirects(), delegateQueue: nil)
    }

    func status() async throws -> WorkspaceSnapshot {
        let status: WorkspaceSnapshot = try await get("status")
        guard status.version == 3 else { throw WorkspaceAPIError(message: "The app and helper need to be updated together.") }
        return status
    }
    func run(_ request: RunRequest) async throws -> WorkspaceRun { try await post(request.prepareOnly ? "prepare" : "run", request) }
    func stop(path: String) async throws -> WorkspaceRun { try await post("stop", PathRequest(path: path)) }
    func choices(path: String, choices: WorkspaceChoices) async throws {
        struct Request: Encodable { let path: String; let target: String; let services: [String] }
        _ = try await request("choices", body: JSONEncoder().encode(Request(path: path, target: choices.target, services: choices.services)))
    }
    func logs(path: String) async throws -> WorkspaceLog { try await get("logs", path: path) }
    func configuration() async throws -> String {
        let data = try await request("config")
        let value = try JSONSerialization.jsonObject(with: data)
        return String(decoding: try JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]), as: UTF8.self)
    }
    func saveConfiguration(_ json: String) async throws {
        let data = Data(json.utf8)
        _ = try JSONSerialization.jsonObject(with: data)
        _ = try await request("config", body: data)
    }
    func prunePlan(path: String) async throws -> PrunePlan { try await get("prune-plan", path: path) }
    func prune(path: String) async throws { _ = try await request("prune", body: JSONEncoder().encode(PathRequest(path: path))) }
    func create(_ request: CreateRequest) async throws { _ = try await self.request("create", body: JSONEncoder().encode(request)) }

    private struct PathRequest: Encodable { let path: String }
    private struct Descriptor: Decodable { let version: Int; let endpoint: String; let instanceId: String }
    private struct Failure: Decodable { let error: String }

    private func get<T: Decodable>(_ route: String, path: String? = nil) async throws -> T {
        try Self.decode(T.self, data: await request(route, path: path))
    }
    private func post<T: Decodable>(_ route: String, _ value: some Encodable) async throws -> T {
        try Self.decode(T.self, data: await request(route, body: JSONEncoder().encode(value)))
    }
    static func decode<T: Decodable>(_ type: T.Type, data: Data) throws -> T {
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { value in
            let text = try value.singleValueContainer().decode(String.self)
            let format = ISO8601DateFormatter()
            format.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = format.date(from: text) { return date }
            format.formatOptions = [.withInternetDateTime]
            guard let date = format.date(from: text) else { throw WorkspaceAPIError(message: "The helper returned an invalid timestamp.") }
            return date
        }
        return try decoder.decode(type, from: data)
    }

    private func request(_ route: String, path: String? = nil, body: Data? = nil) async throws -> Data {
        let directory = root.appending(path: "daemon")
        let descriptor: Descriptor
        let token: String
        do {
            descriptor = try JSONDecoder().decode(Descriptor.self, from: Data(contentsOf: directory.appending(path: "runtime.json")))
            token = try String(contentsOf: directory.appending(path: "token"), encoding: .utf8).trimmingCharacters(in: .whitespacesAndNewlines)
        } catch { throw WorkspaceAPIError(message: "The helper is not connected. Open Setup to repair it.") }
        guard descriptor.version == 3,
              var url = URLComponents(string: descriptor.endpoint), url.scheme == "http", url.host == "127.0.0.1",
              let port = url.port, (1...65535).contains(port), url.user == nil, url.password == nil,
              url.path.isEmpty, url.query == nil, url.fragment == nil, !token.isEmpty else {
            throw WorkspaceAPIError(message: "The helper connection is invalid. Open Setup to repair it.")
        }
        url.path = "/api/\(route)"
        url.queryItems = path.map { [URLQueryItem(name: "path", value: $0)] }
        guard let endpoint = url.url else { throw WorkspaceAPIError(message: "The helper address is invalid.") }
        var request = URLRequest(url: endpoint)
        request.httpMethod = body == nil ? "GET" : "POST"
        request.httpBody = body
        request.timeoutInterval = 30
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("3", forHTTPHeaderField: "X-Switchyard-Version")
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw WorkspaceAPIError(message: "The helper did not return a response.") }
        guard (200..<300).contains(http.statusCode) else {
            let reason = (try? JSONDecoder().decode(Failure.self, from: data))?.error ?? "The helper could not complete the request (\(http.statusCode))."
            throw WorkspaceAPIError(message: reason)
        }
        return data
    }
}

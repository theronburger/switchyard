import Foundation
import Observation
import Sparkle

@MainActor
@Observable
final class AppUpdateController: NSObject, SPUUpdaterDelegate {
    private(set) var availableVersion: String?
    private(set) var isChecking = false
    private(set) var lastCheckedAt: Date?
    private(set) var automaticallyChecksForUpdates = true

    @ObservationIgnored
    private lazy var controller = SPUStandardUpdaterController(
        startingUpdater: true,
        updaterDelegate: self,
        userDriverDelegate: nil
    )

    var buttonTitle: String {
        if let availableVersion { return "Update to Switchyard \(availableVersion)…" }
        return isChecking ? "Checking for Updates…" : "Check for Updates…"
    }

    var canCheckForUpdates: Bool {
        !isChecking && controller.updater.canCheckForUpdates
    }

    func start() {
        guard Bundle.main.bundleURL.pathExtension == "app" else { return }
        _ = controller
        automaticallyChecksForUpdates = controller.updater.automaticallyChecksForUpdates
    }

    func checkForUpdates() {
        guard controller.updater.canCheckForUpdates else { return }
        isChecking = true
        controller.checkForUpdates(nil)
    }

    func setAutomaticUpdateChecks(_ enabled: Bool) {
        controller.updater.automaticallyChecksForUpdates = enabled
        automaticallyChecksForUpdates = enabled
    }

    func updater(_ updater: SPUUpdater, didFindValidUpdate item: SUAppcastItem) {
        availableVersion = item.displayVersionString
        isChecking = false
        lastCheckedAt = Date()
    }

    func updaterDidNotFindUpdate(_ updater: SPUUpdater, error: Error) {
        availableVersion = nil
        isChecking = false
        lastCheckedAt = Date()
    }

    func updater(_ updater: SPUUpdater, didAbortWithError error: Error) {
        isChecking = false
        lastCheckedAt = Date()
    }
}

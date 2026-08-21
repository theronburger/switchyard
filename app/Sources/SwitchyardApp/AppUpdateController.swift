import Foundation
import Observation
import Sparkle
import SwitchyardKit

@MainActor
@Observable
final class AppUpdateController: NSObject, SPUUpdaterDelegate {
    private(set) var availableVersion: String?
    private(set) var isChecking = false
    private(set) var lastCheckedAt: Date?
    private(set) var automaticallyChecksForUpdates = false

    /// Sparkle runs only for a packaged release-channel bundle. Development
    /// builds and bare test executables never instantiate the updater, so they
    /// cannot contact the release appcast or mutate Sparkle's defaults.
    let isAvailable: Bool

    @ObservationIgnored
    private lazy var controller = SPUStandardUpdaterController(
        startingUpdater: true,
        updaterDelegate: self,
        userDriverDelegate: nil
    )

    init(
        channel: SwitchyardChannel = SwitchyardChannel.resolve(),
        isPackagedBundle: Bool = Bundle.main.bundleURL.pathExtension == "app"
    ) {
        isAvailable = isPackagedBundle && channel.permitsUpdates
        super.init()
    }

    var buttonTitle: String {
        if let availableVersion { return "Update to Switchyard \(availableVersion)…" }
        return isChecking ? "Checking for Updates…" : "Check for Updates…"
    }

    var canCheckForUpdates: Bool {
        isAvailable && !isChecking && controller.updater.canCheckForUpdates
    }

    var unavailableReason: String {
        "Updates are delivered only to the signed release build installed from the Homebrew Cask."
    }

    func start() {
        guard isAvailable else { return }
        _ = controller
        automaticallyChecksForUpdates = controller.updater.automaticallyChecksForUpdates
    }

    func checkForUpdates() {
        guard isAvailable, controller.updater.canCheckForUpdates else { return }
        isChecking = true
        controller.checkForUpdates(nil)
    }

    func setAutomaticUpdateChecks(_ enabled: Bool) {
        guard isAvailable else { return }
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

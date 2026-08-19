'use strict';

ObjC.import('AppKit');
ObjC.import('Foundation');

function runningApplications(bundleIdentifier) {
  return $.NSRunningApplication.runningApplicationsWithBundleIdentifier(bundleIdentifier);
}

function run(argv) {
  if (argv.length !== 1) {
    throw new Error('expected one bundle identifier');
  }

  const bundleIdentifier = argv[0];
  const applications = runningApplications(bundleIdentifier);
  for (let index = 0; index < applications.count; index += 1) {
    applications.objectAtIndex(index).terminate();
  }

  for (let attempt = 0; attempt < 100; attempt += 1) {
    if (Number(runningApplications(bundleIdentifier).count) === 0) {
      return;
    }
    $.NSThread.sleepForTimeInterval(0.1);
  }

  throw new Error(`application ${bundleIdentifier} did not terminate`);
}

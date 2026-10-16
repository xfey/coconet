# Claude SDK build dependency

The release builder installs this locked package with `npm ci --ignore-scripts`.
It copies the SDK entry module and its upstream license into client bundles.
This is a build input, not an independent application or model test.

The same SDK enables the offline native-history test in `adapters/claude`.
Synthetic histories are read without invoking a model. SDK code and transitive
packages keep their upstream terms; they are not covered by Coconet's MIT license.

##### Description:

`evergage/evergage-product` uses a single maven version, and to build the project we'll have to manually look at files
that changed and then craft a mvn command to install command. But using `mvngit` will build only modules that need to be built instead of building all modules.

##### How does this work:

1. If there is no previous build info then `mvngit` suggests to build using a generic `mvn install` command that builds all modules
2. If a build info is found in `analytics/server/target/apptegic/WEB-INF/classes/ServerBuildInfo.properties` then it computes the diff (only files that changed)
to check which modules to be built.
# Integration Tests: Windows Local Setup
**Prerequisites**: 
- You've setup Go and the buildkit repo and you can build from source.
- Optional: You have installed the [Delve debugger](https://github.com/go-delve/delve)

### Main Steps
- Install `registry.exe` by running the following command:

  ```go install -v github.com/distribution/distribution/v3/cmd/registry@latest```
- Install buildkit with the following command: 
     ```
    cd to repo directory
    go install -v .\cmd\...
    ```

- The buildkit binary will be installed in `$env:GOPATH\bin` directory. Remove any other buildkit binary available in `$env:PATH` so that it doesn't get picked up by the test suite.
    >NOTE: You can use `Get-Command buildkitd` to see where buildkit is installed. If it doesn't return an equivalent of `$env:GOPATH\bin\buildkitd.exe` delete any other buildkit installations or remove them from `env:PATH`
- In order to run a test we'll focus on the tests written in the `frontend/dockerfile/dockerfile_test.go` file.
- Pick a specific test to run, eg. _testEnvEmptyFormatting_. This is how you run it: 
   > Note the **capitalization** of the test.
    ```
    # cd to the package you want to test, e.g. frontend/dockerfile
    cd frontend/dockerfile
    # run a specific test
    go test -v --timeout=60m --run=TestIntegration/TestEnvEmptyFormatting/worker=containerd 
    ```
- From the sample above `TestIntegration` is the name of the method that runs **all** the tests. `TestEnvEmptyFormatting` is the name of the specific method that contains the tests that we'd like to run.
- `--timeout=60m` might not be needed when running only one test but, its necessary for running a huge testsuite. It can be left out.

### Debugging Tests
#### Terminal

There are times that you might need to debug the tests. The same test above can be executed with Delve (make sure you have Delve installed). 

> **NOTE**: Use a CMD terminal other than a PowerShell terminal
```
    dlv test -- -test.run=TestIntegration/TestEnvEmptyFormatting/worker=containerd
    
    # then proceed to add breakpoint in the test as need be
    (dlv) b frontend\dockerfile\dockerfile_test.go:334
    Breakpoint 1 set at 0x20acfd6 for github.com/moby/buildkit/frontend/dockerfile.testEnvEmptyFormatting() C:/dev/core-containers/buildkit/frontend/dockerfile/dockerfile_test.go:334
    (dlv) c
    time="2024-07-01T14:46:21+03:00" level=info msg="OCI Worker not supported on Windows."
    > [Breakpoint 1] github.com/moby/buildkit/frontend/dockerfile.testEnvEmptyFormatting() C:/dev/core-containers/buildkit/frontend/dockerfile/dockerfile_test.go:334 (hits goroutine(51):1 total:1) (PC: 0x1aecfd6)
    329:                         require.Equal(t, x.expected, string(dt))
    330:                 })
    331:         }
    332: }
    333:
    => 334: func testEnvEmptyFormatting(t *testing.T, sb integration.Sandbox) {
    335:         f := getFrontend(t, sb)
    336:
    337:         dockerfile := []byte(integration.SelectTestCase(t, []string{
    338:                 `
    339: FROM busybox AS build
    (dlv)
```
#### Visual Studio Code
For debugging the tests in VS Code copy the following configuration into your `launch.json` file and run the debugger.

```
{    
    "version": "0.2.0",
    "configurations": [ {
        "name": "Test buildkit tests",
        "type": "go",
        "request": "launch",
        "mode": "test",
        "program": "${workspaceFolder}\\frontend\\dockerfile\\dockerfile_test.go",
        "showLog": true,
        "args":["-test.run=NameOfPackage/NameOfTestMethod/worker=containerd"]
    } ]
}
```
#### Goland
Copy the following configurations to the Run/Debug configurations pop-up window. Each of the following items consists of the label and input for that label - leave everything else as blank or with its default value. 

```
Test framework    => gotest
Test kind         => Package
Package path      => github.com/moby/buildkit/frontend/dockerfile
Pattern           => ^\QTestIntegration\E$ 
Working directory => C:\buildkit\frontend\dockerfile (this is the full path of the package)
Program arguments => -test.run=^TestIntegration/NameOfTestMethod/.*
```

> Name of the method being tested **starts with a capital letter** for both VS Code and Goland. Replace _NameOfTestMethod_ with the specific method you're testing.

### Running the whole test-suite
Running the whole test suite can take a long time, and it's not advisable to do it on your local machine. However, if you'd like to run it, you can do so with the following command:

> Make sure to install gotestsum by running: `go install gotest.tools/gotestsum@latest`
 ```
gotestsum `
--jsonfile="./bin/testreports/go-test-report-test-windows-amd64-frontenddockerfile--containerd.json" `
--junitfile="./bin/testreports/junit-report-test-windows-amd64-frontenddockerfile--containerd.xml"
--packages="./frontend/dockerfile" `
-- "-mod=vendor" "-coverprofile" "./bin/testreports/coverage-test-windows-amd64-frontenddockerfile--containerd.txt"  "-covermode" "atomic" `
-v --parallel=6 --timeout=60m `
**--run=TestIntegration/.*/worker=containerd**
```

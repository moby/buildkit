<!-- START doctoc generated TOC please keep comment here to allow auto update -->
<!-- DON'T EDIT THIS SECTION, INSTEAD RE-RUN doctoc TO UPDATE -->
<!-- details here: https://github.com/thlorenz/doctoc -->

**Table of Contents**

- [Experimental Windows containers support](#experimental-windows-containers-support)
  - [Quick start guide](#quick-start-guide)
    - [Requirements](#platform-requirements)
    - [1. Enable Windows Containers](#1-enable-windows-containers)
    - [2. Install Containerd](#2-install-containerd)
    - [3. Install Buildkit](#3-install-buildkit)
    - [4. Build Simple Hello World Image](#4-build-simple-hello-world-image)

<!-- END doctoc generated TOC please keep comment here to allow auto update -->

# Windows Containers Support in Docker [Experimental]

As from `v0.13`, Buildkit now has experimental support for Windows containers (WCOW). Both `buildctl.exe` and `buildkitd.exe` binaries are being released for testing purposes.

We will apprecate any feedback by [opening an issue here](https://github.com/moby/buildkit/issues/new), as we stabilize the product, especially `buildkitd.exe`.

## Quick start guide

### Requirements

- Architecture: `amd64`, `arm64` (binaries available but not officially tested yet).
- Supported OS: Windows Server 2019, Windows Server 2022, Windows 11.
- Base images: `ServerCore:ltsc2019`, `ServerCore:ltsc2022`, `NanoServer:ltsc2022`. See the [compatibility map here](https://learn.microsoft.com/en-us/virtualization/windowscontainers/deploy-containers/version-compatibility?tabs=windows-server-2019%2Cwindows-11#windows-server-host-os-compatibility).
- `Docker Desktop` v4.29+

### 1. Enable Windows Containers

> **NOTE:** all these requires running as admin (elevated) on a PowerShell terminal.

Make sure that `Containers` feature is enabled. (_`Microsoft-Hyper-V` is a bonus but not necessarily needed for our current guide. Also it's depended on your virtualization platform setup._) Run:

```powershell
Enable-WindowsOptionalFeature -Online -FeatureName Microsoft-Hyper-V, Containers -All
```

If you see RestartNeeded as True on your setup, restart your machine and reopen an Administrator PowerShell terminal. Otherwise, continue to the next step.

### 2. Install Containerd

The detailed `containerd` setup [instructions are here](https://github.com/containerd/containerd/blob/main/docs/getting-started.md#installing-containerd-on-windows). (_Currently, we only support the `containerd` worker_.)
Run the following script to install the latest containerd release. If you have containerd already installed, skip the script below and run Start-Service containerd to start the containerd service. Note: containerd v1.7.7+ is required.

1. If containerd previously installed run

   ```powershell
   Stop-Service containerd
   ```

1. Download and extract desired containerd Windows binaries

   ```powershell
   $Version="1.7.13" # update to your preferred version
   curl.exe -L https://github.com/containerd/containerd/releases/download/v$Version/containerd-$Version-windows-amd64.tar.gz -o containerd-windows-amd64.tar.gz
   tar.exe xvf .\containerd-windows-amd64.tar.gz
   ```

1. Copy and configure

   ```powershell
   Copy-Item -Path ".\bin" -Destination "$Env:ProgramFiles\containerd" -Recurse -Container:$false -Force
   cd $Env:ProgramFiles\containerd\
   .\containerd.exe config default | Out-File config.toml -Encoding ascii
   ```

1. Copy

   ```powershell
   Copy-Item -Path .\bin\* -Destination (New-Item -Type Directory $Env:ProgramFiles\containerd -Force) -Recurse -Force
   ```

1. add the binaries (containerd.exe, ctr.exe) in $env:Path

   ```powershell
   $Path = [Environment]::GetEnvironmentVariable("PATH", "Machine") + [IO.Path]::PathSeparator + "$Env:ProgramFiles\containerd"
   [Environment]::SetEnvironmentVariable( "Path", $Path, "Machine")
   ```

1. reload path, so you don't have to open a new PS terminal later if needed

   ```powershell
   $Env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine") + ";" + [System.Environment]::GetEnvironmentVariable("Path","User")
   ```

1. configure

   ```powershell
   containerd.exe config default | Out-File $Env:ProgramFiles\containerd\config.toml -Encoding ascii
   # Review the configuration. Depending on setup you may want to adjust:
   # - the sandbox_image (Kubernetes pause image)
   # - cni bin_dir and conf_dir locations
   Get-Content $Env:ProgramFiles\containerd\config.toml
   ```

1. Register and start service
   ```powershell
   containerd.exe --register-service
   Start-Service containerd
   ```

### 3. Install Buildkit

Run the following script to download and extract the latest BuildKit release.

1. Download and extract:
   ```powershell
   $version = "v0.13.1" # specify the release version, v0.13+
   $arch = "amd64" # arm64 binary available too
   curl.exe -LO https://github.com/moby/buildkit/releases/download/$version/buildkit-$version.windows-$arch.tar.gz
   # there could be another `.\bin` directory from containerd instructions
   # you can move those
   mv bin bin2
   tar.exe xvf .\buildkit-$version.windows-$arch.tar.gz
   ## x bin/
   ## x bin/buildctl.exe
   ## x bin/buildkitd.exe
   ```
1. Setup `buildkit` binaries:
   ```powershell
   # after the binaries are extracted in the bin directory
   # move them to an appropriate path in your $Env:PATH directories or:
   Copy-Item -Path ".\bin" -Destination "$Env:ProgramFiles\buildkit" -Recurse -Force
   # add `buildkitd.exe` and `buildctl.exe` binaries in the $Env:PATH
   $Path = [Environment]::GetEnvironmentVariable("PATH", "Machine") + `
       [IO.Path]::PathSeparator + "$Env:ProgramFiles\buildkit"
   [Environment]::SetEnvironmentVariable( "Path", $Path, "Machine")
   $Env:Path = [System.Environment]::GetEnvironmentVariable("Path","Machine") + ";" + `
       [System.Environment]::GetEnvironmentVariable("Path","User")
   ```
1. Start `buildkitd.exe`, you should see something similar to:

   ```
   PS C:\> buildkitd.exe
   time="2024-02-26T10:42:16+03:00" level=warning msg="using null network as the default"
   time="2024-02-26T10:42:16+03:00" level=info msg="found worker \"zcy8j5dyjn3gztjv6gv9kn037\", labels=map[org.mobyproject.buildkit.worker.containerd.namespace:buildkit org.mobyproject.buildkit.worker.containerd.uuid:c30661c1-5115-45de-9277-a6386185a283 org.mobyproject.buildkit.worker.executor:containerd org.mobyproject.buildkit.worker.hostname:[deducted] org.mobyproject.buildkit.worker.network: org.mobyproject.buildkit.worker.selinux.enabled:false org.mobyproject.buildkit.worker.snapshotter:windows], platforms=[windows/amd64]"
   time="2024-02-26T10:42:16+03:00" level=info msg="found 1 workers, default=\"zcy8j5dyjn3gztjv6gv9kn037\""
   time="2024-02-26T10:42:16+03:00" level=warning msg="currently, only the default worker can be used."
   time="2024-02-26T10:42:16+03:00" level=info msg="running server on //./pipe/buildkitd"
   ```

1. In another terminal (still elevated), we can setup `Buildx` command to use our Buildkit instance:

   ```powershell
   PS> C:\> docker buildx create --name buildkit-exp --use --driver=remote npipe:////./pipe/buildkitd

   buildkit-exp
   ```

1. Let's test our connection with `buildx inspect`

```powershell
   PS> C:\> docker buildx inspect

   Name:          buildkit-exp
    Driver:        remote
    Last Activity: 2024-03-27 17:51:58 +0000 UTC

    Nodes:
    Name:             buildkit-exp0
    Endpoint:         npipe:////./pipe/buildkitd
    Status:           running
    BuildKit version: v0.13.0-rc2
    Platforms:        windows/amd64
    Labels:
    org.mobyproject.buildkit.worker.containerd.namespace: buildkit
    org.mobyproject.buildkit.worker.containerd.uuid:      b915c84b-74d8-464c-8360-ae442adcf7a2
    org.mobyproject.buildkit.worker.executor:             containerd
    org.mobyproject.buildkit.worker.hostname:             colin-win11
    org.mobyproject.buildkit.worker.network:
    org.mobyproject.buildkit.worker.selinux.enabled:      false
    org.mobyproject.buildkit.worker.snapshotter:          windows
    GC Policy rule#0:
    All:           false
    Filters:       type==source.local,type==exec.cachemount,type==source.git.checkout
    Keep Duration: 48h0m0s
    Keep Bytes:    488.3MiB
    GC Policy rule#1:
    All:           false
    Keep Duration: 1440h0m0s
    Keep Bytes:    1.863GiB
    GC Policy rule#2:
    All:        false
    Keep Bytes: 1.863GiB
    GC Policy rule#3:
    All:        true
    Keep Bytes: 1.863GiB
```

### 4. Build Simple Hello World Image

Now that everything is setup, let's build a [simple _hello world_ image](https://github.com/docker-library/hello-world/blob/master/amd64/hello-world/nanoserver-ltsc2022/Dockerfile).

1. Create a directory called `sample_dockerfile`:

   ```powershell
   mkdir sample_dockerfile
   cd sample_dockerfile
   ```

1. Inside it, add files `dockerfile` and `hello.txt`:

   ```powershell
   Set-Content Dockerfile @"
   FROM mcr.microsoft.com/windows/nanoserver:ltsc2022
   USER ContainerAdministrator
   COPY hello.txt C:/
   RUN echo "Goodbye!" >> hello.txt
   CMD ["cmd", "/C", "type C:\\hello.txt"]
   "@

   Set-Content hello.txt @"
   Hello from buildkit!
   This message shows that your installation appears to be working correctly.
   "@
   ```

   > **NOTE:** For a consistent experience, just use the front-slashes `/` for paths, e.g. `C:/` instead of `C:\`. We are working to fix this, see [#4696](https://github.com/moby/buildkit/issues/4696).

1. Build and push to your registry (or set to `push=false`). For Docker Hub, make sure you've done `docker login`. See more details on registry configuration [here](../README.md#imageregistry)

   ```powershell
   docker buildx build --push -t <your_username>/hello-buildkit .
   ```

   You should see a similar output:

   ```
   [+] Building 45.6s (9/9) FINISHED                                     remote:buildkit-exp
   => [internal] load build definition from Dockerfile                                  0.2s
   => => transferring dockerfile: 211B                                                  0.0s
   => [internal] load metadata for mcr.microsoft.com/windows/nanoserver:ltsc2022        0.7s
   => [internal] load .dockerignore                                                     0.1s
   => => transferring context: 2B                                                       0.0s
   => [internal] load build context                                                     0.2s
   => => transferring context: 30B                                                      0.0s
   => [1/3] FROM mcr.microsoft.com/windows/nanoserver:ltsc2022@sha256:6223...66d       26.0s
   => => resolve mcr.microsoft.com/windows/nanoserver:ltsc2022@sha256:6223...66d        0.1s
   => => sha256:b475...7b5 117.22MB / 117.22MB                                          3.7s
   => => extracting sha256:b475...7b5                                                  22.0s
   => [2/3] COPY hello.txt C:/                                                          0.5s
   => [3/3] RUN echo "Goodbye!" >> hello.txt                                            6.5s
   => exporting to image                                                                9.7s
   => => exporting layers                                                               2.6s
   => => exporting manifest sha256:29e4...636                                           0.0s
   => => exporting config sha256:1358...c31                                             0.0s
   => => exporting attestation manifest sha256:a9b0...193                               0.1s
   => => exporting manifest list sha256:a09d...ab5                                        0s
   => => naming to xxxxxxxxxx/hello-buildkit                                            0.0s
   => => pushing layers                                                                 5.6s
   => => pushing manifest for docker.io/**/hello-buildkit:latest@sha256:a09d...ab5      1.2s
   => [auth] gonzohunter/hello-buildkit:pull,push token for registry-1.docker.io        0.0s

   View build details: docker-desktop://dashboard/build/buildkit-exp/buildkit-exp0/r8j6i2t3on4fj7fb1gvi20kmk
   ```

> **NOTE:** After pushing to the registry, you can use your image with any other clients to spin off containers, e.g. `docker run`, `ctr run`, `nerdctl run`, etc.

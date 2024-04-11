# buildkit 变更记录

官方地址：[buildkit](https://github.com/moby/buildkit/tree/v0.13.1)

- 自动识别 http/https 仓库，默认使用insecure client请求。
  - [DEVOPS-19463](https://jira.alauda.cn/browse/DEVOPS-19463) pull http仓库失败问题
  - [DEVOPS-19601](https://jira.alauda.cn/browse/DEVOPS-19601) 连接自签名https仓库拉取失败问题

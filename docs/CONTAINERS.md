# Docker 容器清单

仅主环境四个容器运行；测试和恢复环境已停止，数据卷保留供复核。

| 容器 | 状态 | 端口显示 |
|---|---|---|
| ai-gateway-nginx-1 | running | 127.0.0.1:80->80/tcp |
| gw-restore-20260926101309-nginx-1 | exited | 无活动发布端口 |
| gw-restore-20260926101309-new-api-1 | exited | 无活动发布端口 |
| gw-restore-20260926101309-database-1 | exited | 无活动发布端口 |
| gw-restore-20260926101309-redis-1 | exited | 无活动发布端口 |
| ai-gateway-new-api-1 | running | 3000/tcp |
| gw-restore-20260926101046-nginx-1 | exited | 无活动发布端口 |
| gw-restore-20260926101046-new-api-1 | exited | 无活动发布端口 |
| gw-restore-20260926101046-database-1 | exited | 无活动发布端口 |
| gw-restore-20260926101046-redis-1 | exited | 无活动发布端口 |
| gw-restore-20260926043031-nginx-1 | exited | 无活动发布端口 |
| gw-restore-20260926043031-new-api-1 | exited | 无活动发布端口 |
| gw-restore-20260926043031-database-1 | exited | 无活动发布端口 |
| gw-restore-20260926043031-redis-1 | exited | 无活动发布端口 |
| mock-1790368143-nginx-1 | exited | 无活动发布端口 |
| mock-1790368143-new-api-1 | exited | 无活动发布端口 |
| mock-1790368143-mock-a-1 | exited | 无活动发布端口 |
| mock-1790368143-database-1 | exited | 无活动发布端口 |
| mock-1790368143-redis-1 | exited | 无活动发布端口 |
| mock-1790368143-mock-b-1 | exited | 无活动发布端口 |
| gw-restore-20260926042843-nginx-1 | exited | 无活动发布端口 |
| gw-restore-20260926042843-new-api-1 | exited | 无活动发布端口 |
| gw-restore-20260926042843-database-1 | exited | 无活动发布端口 |
| gw-restore-20260926042843-redis-1 | exited | 无活动发布端口 |
| gw-restore-20260926042804-nginx-1 | exited | 无活动发布端口 |
| gw-restore-20260926042804-new-api-1 | exited | 无活动发布端口 |
| gw-restore-20260926042804-database-1 | exited | 无活动发布端口 |
| gw-restore-20260926042804-redis-1 | exited | 无活动发布端口 |
| mock-1790367997-nginx-1 | exited | 无活动发布端口 |
| mock-1790367997-new-api-1 | exited | 无活动发布端口 |
| mock-1790367997-mock-a-1 | exited | 无活动发布端口 |
| mock-1790367997-database-1 | exited | 无活动发布端口 |
| mock-1790367997-redis-1 | exited | 无活动发布端口 |
| mock-1790367997-mock-b-1 | exited | 无活动发布端口 |
| mock-1790367854-nginx-1 | exited | 无活动发布端口 |
| mock-1790367854-new-api-1 | exited | 无活动发布端口 |
| mock-1790367854-mock-b-1 | exited | 无活动发布端口 |
| mock-1790367854-database-1 | exited | 无活动发布端口 |
| mock-1790367854-redis-1 | exited | 无活动发布端口 |
| mock-1790367854-mock-a-1 | exited | 无活动发布端口 |
| mock-1790345603-nginx-1 | exited | 无活动发布端口 |
| mock-1790345603-new-api-1 | exited | 无活动发布端口 |
| mock-1790345603-mock-a-1 | exited | 无活动发布端口 |
| mock-1790345603-database-1 | exited | 无活动发布端口 |
| mock-1790345603-redis-1 | exited | 无活动发布端口 |
| mock-1790345603-mock-b-1 | exited | 无活动发布端口 |
| ai-gateway-database-1 | running | 5432/tcp |
| ai-gateway-redis-1 | running | 6379/tcp |

// Package sources 数据源导入集合：本文件的空白导入清单即「本部署携带哪些源」。
// 新增/下线数据源只改本文件（加/删一行空白导入）+ 对应源包（sources/<源>）；
// app.go 与 seed.go 免改——路由对账与 seed 播种均以各源 base.RegisterSource 声明为准。
//
// 底座项目不含任何数据源：本文件保持空导入，接入书源时在此登记。
package sources

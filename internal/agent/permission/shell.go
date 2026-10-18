package permission

// ShellClassification 不依据命令文本授信。Bash 继承宿主环境，字面量内建命令
// 也可能被导出函数或 BASH_ENV 替换；受限模式下一律审批。
func ShellClassification(_ string) Classification {
	return Classification{Identity: "builtin:bash", Effect: EffectUnknown}
}

package v1

import (
	"github.com/gin-gonic/gin"
	apiv1 "github.com/lyonmu/kaguya/internal/api/v1"
)

type MemoryRouter struct{}

func (r *MemoryRouter) InitMemoryRouter(group *gin.RouterGroup, apiGroup apiv1.ApiV1Group) {
	memoryRouter := group.Group("v1/memory")
	{
		memoryRouter.GET("pages", apiGroup.MemoryPageList)
		memoryRouter.GET("pages/:id", apiGroup.MemoryPageDetail)
		memoryRouter.POST("pages", apiGroup.MemoryPageCreate)
		memoryRouter.PATCH("pages/:id", apiGroup.MemoryPageUpdate)
		memoryRouter.DELETE("pages/:id", apiGroup.MemoryPageDelete)
		memoryRouter.GET("pages/:id/revisions", apiGroup.MemoryPageRevisions)
		memoryRouter.GET("pages/:id/diff", apiGroup.MemoryPageDiff)
		memoryRouter.POST("pages/:id/restore", apiGroup.MemoryPageRestore)
		memoryRouter.GET("sources", apiGroup.MemorySourceList)
		memoryRouter.GET("sources/:id", apiGroup.MemorySourceDetail)
		memoryRouter.POST("backfill", apiGroup.MemoryBackfill)
		memoryRouter.POST("import", apiGroup.MemoryImport)
		memoryRouter.GET("jobs", apiGroup.MemoryJobList)
		memoryRouter.POST("jobs/:id/retry", apiGroup.MemoryJobRetry)
		memoryRouter.POST("jobs/:id/approve", apiGroup.MemoryJobApprove)
		memoryRouter.POST("jobs/:id/reject", apiGroup.MemoryJobReject)
		memoryRouter.POST("compile", apiGroup.MemoryCompile)
		memoryRouter.GET("status", apiGroup.MemoryStatus)
		memoryRouter.POST("export", apiGroup.MemoryExport)
	}
}

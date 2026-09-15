import { lazy, Suspense } from "react";
import { Spin, Tabs } from "antd";

const ProviderManagementPage = lazy(() =>
  import("./ProviderManagementPage").then((module) => ({
    default: module.ProviderManagementPage,
  })),
);
const ProviderCatalogPage = lazy(() =>
  import("./ProviderCatalogPage").then((module) => ({
    default: module.ProviderCatalogPage,
  })),
);
const ModelCatalogPage = lazy(() =>
  import("./ModelCatalogPage").then((module) => ({
    default: module.ModelCatalogPage,
  })),
);
const MCPManagementPanel = lazy(() =>
  import("./MCPManagementPanel").then((module) => ({
    default: module.MCPManagementPanel,
  })),
);

export function AIConfigurationPage() {
  return (
    <Suspense
      fallback={
        <div className="p-8 text-center">
          <Spin />
        </div>
      }
    >
      <Tabs
        className="w-full"
        tabBarStyle={{ padding: "8px 24px 0", marginBottom: 0 }}
        destroyOnHidden
        items={[
          {
            key: "providers",
            label: "提供商与模型",
            children: <ProviderManagementPage />,
          },
          { key: "mcp", label: "MCP 管理", children: <MCPManagementPanel /> },
          {
            key: "provider-catalog",
            label: "提供商目录",
            children: <ProviderCatalogPage />,
          },
          {
            key: "model-catalog",
            label: "模型目录",
            children: <ModelCatalogPage />,
          },
        ]}
      />
    </Suspense>
  );
}

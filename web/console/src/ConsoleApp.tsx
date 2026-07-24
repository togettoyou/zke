import { Card, Flex, Space, Tag, Typography } from "antd";

const { Paragraph, Text, Title } = Typography;

export function ConsoleApp() {
  return (
    <main className="console-shell">
      <section className="hero">
        <Space direction="vertical" size={18}>
          <Tag color="processing">Phase 1 · 工程骨架</Tag>
          <Title>ZKE Console</Title>
          <Paragraph className="hero-copy">
            ZKE 是构建在 Kubernetes 之上的 AI 原生 Kubernetes
            管理与算力平台。
          </Paragraph>
        </Space>

        <Flex gap={16} wrap>
          <StatusCard title="Server" value="健康检查已就绪" />
          <StatusCard title="Agent" value="进程生命周期已就绪" />
          <StatusCard title="Console" value="基础工作区已就绪" />
        </Flex>

        <Card className="notice-card" bordered={false}>
          <Text strong>当前状态</Text>
          <Paragraph>
            Agent 注册、QUIC/mTLS 心跳、用户认证和 RBAC
            基础已经实现；管理界面与 Kubernetes 资源操作仍在开发中。
          </Paragraph>
        </Card>
      </section>
    </main>
  );
}

type StatusCardProps = {
  title: string;
  value: string;
};

function StatusCard({ title, value }: StatusCardProps) {
  return (
    <Card className="status-card" bordered={false}>
      <Text type="secondary">{title}</Text>
      <Title level={4}>{value}</Title>
    </Card>
  );
}

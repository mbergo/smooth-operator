import React, { useState, useEffect } from 'react';
import { KubeConfig, CustomObjectsApi } from '@kubernetes/client-node';
import './App.css';

interface ChatMessage {
  role: 'user' | 'assistant';
  content: string;
  timestamp: Date;
}

interface ChatSessionStatus {
  state: string;
  reason: string;
  lastUpdated: string;
}

function App() {
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [input, setInput] = useState('');
  const [namespace, setNamespace] = useState('default');
  const [gitRepo, setGitRepo] = useState('git@github.com:example/repo.git');
  const [gitPath, setGitPath] = useState('apps/my-app');
  const [autoMode, setAutoMode] = useState(false);
  const [loading, setLoading] = useState(false);
  const [k8sClient, setK8sClient] = useState<CustomObjectsApi | null>(null);

  useEffect(() => {
    // Initialize Kubernetes client
    try {
      const kc = new KubeConfig();
      kc.loadFromDefault();
      const client = kc.makeApiClient(CustomObjectsApi);
      setK8sClient(client);
    } catch (error) {
      console.error('Failed to initialize K8s client:', error);
    }
  }, []);

  const createChatSession = async (userPrompt: string) => {
    if (!k8sClient) {
      console.error('K8s client not initialized');
      return;
    }

    const timestamp = new Date().toISOString().replace(/[:.]/g, '-').substring(0, 19);
    const sessionName = `chat-${timestamp}`;

    const chatSession = {
      apiVersion: 'smooth.smooth.k8s.io/v1',
      kind: 'ChatSession',
      metadata: {
        name: sessionName,
        namespace: 'default',
      },
      spec: {
        user: 'ui-user@example.com',
        targetNamespace: namespace,
        prompt: userPrompt,
        metadata: {
          gitRepo: gitRepo,
          gitPath: gitPath,
        },
        preferAuto: autoMode,
        createdByUI: true,
      },
    };

    try {
      await k8sClient.createNamespacedCustomObject(
        'smooth.smooth.k8s.io',
        'v1',
        'default',
        'chatsessions',
        chatSession
      );

      console.log('ChatSession created:', sessionName);
      
      // Watch for status updates
      watchChatSession(sessionName);
    } catch (error) {
      console.error('Failed to create ChatSession:', error);
      setMessages(prev => [...prev, {
        role: 'assistant',
        content: `❌ Error: ${error}`,
        timestamp: new Date(),
      }]);
    }
  };

  const watchChatSession = async (name: string) => {
    // Poll for status updates (in production, use watch API or websockets)
    const interval = setInterval(async () => {
      try {
        if (!k8sClient) return;

        const response: any = await k8sClient.getNamespacedCustomObject(
          'smooth.smooth.k8s.io',
          'v1',
          'default',
          'chatsessions',
          name
        );

        const status: ChatSessionStatus = response.body.status;
        
        if (status && (status.state === 'Completed' || status.state === 'Failed')) {
          clearInterval(interval);
          
          setMessages(prev => [...prev, {
            role: 'assistant',
            content: `✅ ${status.state}: ${status.reason}`,
            timestamp: new Date(),
          }]);

          // Check for SmoothAction
          checkSmoothAction(name);
        }
      } catch (error) {
        console.error('Error watching ChatSession:', error);
      }
    }, 2000);

    setTimeout(() => clearInterval(interval), 300000); // 5min timeout
  };

  const checkSmoothAction = async (chatSessionName: string) => {
    try {
      if (!k8sClient) return;

      const actionName = `action-${chatSessionName}`;
      const response: any = await k8sClient.getNamespacedCustomObject(
        'smooth.smooth.k8s.io',
        'v1',
        'default',
        'smoothactions',
        actionName
      );

      const action = response.body;
      
      let content = `\n🎯 **Plan Generated:**\n\n`;
      content += `- Mode: ${action.spec.mode}\n`;
      content += `- Inferred Needs: ${action.spec.inferredNeeds?.length || 0}\n`;
      content += `- Patches: ${action.spec.patches?.length || 0}\n\n`;

      if (action.status?.git?.prURL) {
        content += `📦 **Pull Request**: [View PR](${action.status.git.prURL})\n`;
      }

      setMessages(prev => [...prev, {
        role: 'assistant',
        content,
        timestamp: new Date(),
      }]);
    } catch (error) {
      console.log('SmoothAction not yet available');
    }
  };

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!input.trim() || loading) return;

    const userMessage: ChatMessage = {
      role: 'user',
      content: input,
      timestamp: new Date(),
    };

    setMessages(prev => [...prev, userMessage]);
    setInput('');
    setLoading(true);

    // Add "thinking" message
    setMessages(prev => [...prev, {
      role: 'assistant',
      content: '🤔 Analyzing cluster and generating recommendations...',
      timestamp: new Date(),
    }]);

    await createChatSession(input);
    setLoading(false);
  };

  return (
    <div className="App">
      <header className="App-header">
        <h1>🤖 Smooth Operator</h1>
        <p>Conversational GitOps for Kubernetes</p>
      </header>

      <div className="config-panel">
        <input
          type="text"
          placeholder="Target Namespace"
          value={namespace}
          onChange={(e) => setNamespace(e.target.value)}
        />
        <input
          type="text"
          placeholder="Git Repository"
          value={gitRepo}
          onChange={(e) => setGitRepo(e.target.value)}
        />
        <input
          type="text"
          placeholder="Git Path"
          value={gitPath}
          onChange={(e) => setGitPath(e.target.value)}
        />
        <label>
          <input
            type="checkbox"
            checked={autoMode}
            onChange={(e) => setAutoMode(e.target.checked)}
          />
          Auto Mode (risk-gated)
        </label>
      </div>

      <div className="chat-container">
        {messages.map((msg, idx) => (
          <div key={idx} className={`message ${msg.role}`}>
            <div className="message-content">{msg.content}</div>
            <div className="message-time">
              {msg.timestamp.toLocaleTimeString()}
            </div>
          </div>
        ))}
      </div>

      <form onSubmit={handleSubmit} className="input-form">
        <input
          type="text"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder="Describe what you want to deploy or fix..."
          disabled={loading}
        />
        <button type="submit" disabled={loading || !input.trim()}>
          {loading ? '⏳ Processing...' : '🚀 Send'}
        </button>
      </form>
    </div>
  );
}

export default App;


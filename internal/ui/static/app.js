// State
let services = [];
let autoRefreshInterval = null;

// DOM Elements
const servicesList = document.getElementById('services-list');
const addServiceBtn = document.getElementById('add-service-btn');
const refreshBtn = document.getElementById('refresh-btn');
const modal = document.getElementById('add-service-modal');
const closeModal = document.querySelector('.close');
const cancelBtn = document.getElementById('cancel-btn');
const addServiceForm = document.getElementById('add-service-form');
const healthEnabled = document.getElementById('health-enabled');
const healthCheckOptions = document.getElementById('health-check-options');
const tlsEnabled = document.getElementById('tls-enabled');
const tlsOptions = document.getElementById('tls-options');

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    loadServices();
    startAutoRefresh();

    // Event listeners
    addServiceBtn.addEventListener('click', () => modal.classList.remove('hidden'));
    closeModal.addEventListener('click', () => modal.classList.add('hidden'));
    cancelBtn.addEventListener('click', () => modal.classList.add('hidden'));
    refreshBtn.addEventListener('click', loadServices);
    addServiceForm.addEventListener('submit', handleAddService);

    healthEnabled.addEventListener('change', (e) => {
        if (e.target.checked) {
            healthCheckOptions.classList.remove('hidden');
        } else {
            healthCheckOptions.classList.add('hidden');
        }
    });

    tlsEnabled.addEventListener('change', (e) => {
        if (e.target.checked) {
            tlsOptions.classList.remove('hidden');
        } else {
            tlsOptions.classList.add('hidden');
        }
    });

    // Close modal when clicking outside
    window.addEventListener('click', (e) => {
        if (e.target === modal) {
            modal.classList.add('hidden');
        }
    });
});

// Load services from API
async function loadServices() {
    try {
        const response = await fetch('/api/services');
        if (!response.ok) throw new Error('Failed to load services');

        services = await response.json();
        renderServices();
    } catch (error) {
        console.error('Error loading services:', error);
        servicesList.innerHTML = `
            <div class="text-center py-12">
                <p class="text-red-600 mb-4">Failed to load services: ${error.message}</p>
                <button onclick="loadServices()" class="px-4 py-2 bg-white border border-gray-300 rounded-lg hover:bg-gray-50">
                    Retry
                </button>
            </div>
        `;
    }
}

// Render services list
function renderServices() {
    if (services.length === 0) {
        servicesList.innerHTML = `
            <div class="text-center py-12 text-gray-500">
                <p class="text-lg mb-2">No services configured yet.</p>
                <p>Click "Add Service" to get started!</p>
            </div>
        `;
        return;
    }

    servicesList.innerHTML = services.map(service => `
        <div class="bg-white rounded-lg shadow hover:shadow-lg transition-shadow p-6 border-l-4 ${service.healthy ? 'border-green-500' : 'border-red-500'}">
            <div class="flex justify-between items-start mb-4">
                <h3 class="text-xl font-bold flex items-center gap-2">
                    <span class="${service.healthy ? 'text-green-500' : 'text-red-500'}">${service.healthy ? '🟢' : '🔴'}</span>
                    ${service.name}
                </h3>
                <button onclick="deleteService('${service.name}')" class="px-4 py-2 bg-red-600 hover:bg-red-700 text-white rounded-lg text-sm font-medium transition">
                    Delete
                </button>
            </div>

            <div class="space-y-2 text-sm">
                <div class="flex gap-4">
                    <span class="font-semibold text-gray-600 min-w-[140px]">Backend:</span>
                    <span class="text-gray-900">${service.backend}</span>
                </div>

                ${service.paths && service.paths.length > 0 ? `
                    <div class="flex gap-4">
                        <span class="font-semibold text-gray-600 min-w-[140px]">Paths:</span>
                        <span class="text-gray-900">${service.paths.join(', ')}</span>
                    </div>
                ` : ''}

                ${service.stripPrefix ? `
                    <div class="flex gap-4">
                        <span class="font-semibold text-gray-600 min-w-[140px]">Strip Prefix:</span>
                        <span class="text-gray-900">Yes</span>
                    </div>
                ` : ''}

                ${service.healthCheck.enabled ? `
                    <div class="flex gap-4">
                        <span class="font-semibold text-gray-600 min-w-[140px]">Health Check:</span>
                        <span class="text-gray-900">${service.healthCheck.path} (${service.healthCheck.interval})</span>
                    </div>
                ` : `
                    <div class="flex gap-4">
                        <span class="font-semibold text-gray-600 min-w-[140px]">Health Check:</span>
                        <span class="text-gray-500">Disabled</span>
                    </div>
                `}

                ${service.tls.enabled ? `
                    <div class="flex gap-4">
                        <span class="font-semibold text-gray-600 min-w-[140px]">Backend TLS:</span>
                        <span class="text-gray-900">Enabled ${service.tls.skipVerify ? '(skip verify)' : ''}</span>
                    </div>
                ` : ''}

                <div class="flex gap-4">
                    <span class="font-semibold text-gray-600 min-w-[140px]">Tailscale URL:</span>
                    <code class="text-indigo-600 bg-gray-50 px-2 py-1 rounded">https://${service.name}.your-tailnet.ts.net</code>
                </div>
            </div>
        </div>
    `).join('');
}

// Handle add service form submission
async function handleAddService(e) {
    e.preventDefault();

    const formData = new FormData(e.target);

    // Parse paths
    const pathsInput = formData.get('paths');
    const paths = pathsInput ? pathsInput.split(',').map(p => p.trim()).filter(p => p) : [];

    // Build service config
    const serviceConfig = {
        name: formData.get('name'),
        backend: formData.get('backend'),
        paths: paths,
        stripPrefix: formData.get('stripPrefix') === 'on',
        healthCheck: {
            enabled: formData.get('healthCheckEnabled') === 'on',
            path: formData.get('healthCheckPath') || '/health',
            interval: formData.get('healthCheckInterval') || '30s',
            timeout: formData.get('healthCheckTimeout') || '5s',
            unhealthyThreshold: parseInt(formData.get('healthCheckThreshold')) || 3
        },
        tls: {
            enabled: formData.get('tlsEnabled') === 'on',
            skipVerify: formData.get('tlsSkipVerify') === 'on'
        }
    };

    try {
        const response = await fetch('/api/services', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(serviceConfig)
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(error);
        }

        // Success - close modal and reload services
        modal.classList.add('hidden');
        addServiceForm.reset();
        await loadServices();

        showNotification(`Service ${serviceConfig.name} added successfully!`, 'success');
    } catch (error) {
        console.error('Error adding service:', error);
        showNotification(`Failed to add service: ${error.message}`, 'error');
    }
}

// Delete service
async function deleteService(name) {
    if (!confirm(`Are you sure you want to delete service "${name}"?`)) {
        return;
    }

    try {
        const response = await fetch(`/api/services/${name}`, {
            method: 'DELETE'
        });

        if (!response.ok) {
            const error = await response.text();
            throw new Error(error);
        }

        await loadServices();
        showNotification(`Service ${name} deleted successfully!`, 'success');
    } catch (error) {
        console.error('Error deleting service:', error);
        showNotification(`Failed to delete service: ${error.message}`, 'error');
    }
}

// Show notification
function showNotification(message, type = 'info') {
    const colors = {
        success: 'bg-green-50 border-l-4 border-green-500 text-green-900',
        error: 'bg-red-50 border-l-4 border-red-500 text-red-900',
        info: 'bg-indigo-50 border-l-4 border-indigo-500 text-indigo-900'
    };

    const notification = document.createElement('div');
    notification.className = `fixed top-4 right-4 p-4 rounded-lg shadow-lg transition-transform transform translate-x-full ${colors[type]} z-50`;
    notification.textContent = message;
    document.body.appendChild(notification);

    setTimeout(() => {
        notification.classList.remove('translate-x-full');
    }, 10);

    setTimeout(() => {
        notification.classList.add('translate-x-full');
        setTimeout(() => {
            document.body.removeChild(notification);
        }, 300);
    }, 3000);
}

// Auto-refresh services every 5 seconds
function startAutoRefresh() {
    autoRefreshInterval = setInterval(loadServices, 5000);
}

// Stop auto-refresh
function stopAutoRefresh() {
    if (autoRefreshInterval) {
        clearInterval(autoRefreshInterval);
    }
}

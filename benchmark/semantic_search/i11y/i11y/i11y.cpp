#include <Windows.h>
#include <OleAcc.h>
#include <iostream>
#include <string>
#include <vector>
#include <UIAutomation.h>
#include <atlbase.h>

#pragma comment(lib, "Oleacc.lib")
#pragma comment(lib, "uiautomationcore.lib")

const std::wstring kTargetControlName = L"Chat Input, Type to ask questions or type / for topics";

const std::wstring kTargetButtonName = L"Send ";
const std::wstring kCancelButtonName = L"Cancel ";
const std::wstring kNewButtonName = L"New Chat";



void ClickButton(IAccessible* pButton) {
    if (!pButton) return;

    VARIANT varSelf;
    VariantInit(&varSelf);
    varSelf.vt = VT_I4;
    varSelf.lVal = CHILDID_SELF;

    pButton->accDoDefaultAction(varSelf);
}

IAccessible* FindAccessibleByNameAndRole(IAccessible* pAcc, const std::wstring& name, long role) {
    if (!pAcc) return nullptr;

    long count = 0;
    pAcc->get_accChildCount(&count);

    VARIANT* children = new VARIANT[count];
    if (!children) return nullptr;

    HRESULT hr = AccessibleChildren(pAcc, 0, count, children, &count);
    IAccessible* result = nullptr;

    for (long i = 0; i < count; ++i) {
        VARIANT var = children[i];
        IAccessible* child = nullptr;
        std::wstring childName;
        long childRole = 0;

        if (var.vt == VT_DISPATCH && var.pdispVal) {
            var.pdispVal->QueryInterface(IID_IAccessible, (void**)&child);
        }

        if (child) {
            // 构造 VARIANT 传入 CHILDID_SELF
            VARIANT varSelf;
            VariantInit(&varSelf);
            varSelf.vt = VT_I4;
            varSelf.lVal = CHILDID_SELF;

            BSTR bName = nullptr;
            VARIANT vRole;
            VariantInit(&vRole);

            child->get_accName(varSelf, &bName);
            child->get_accRole(varSelf, &vRole);

            if (bName) {
                childName = bName;
                SysFreeString(bName);
            }
            childRole = vRole.lVal;
            VariantClear(&vRole);

            if (childName.find(name) != std::wstring::npos && childRole == role) {
                result = child;
                break;
            }
            else {
                result = FindAccessibleByNameAndRole(child, name, role);
                child->Release();
                if (result) break;
            }
        }
    }

    delete[] children;
    return result;
}

IUIAutomation* g_pAutomation = nullptr;
std::wstring BstrToWString(BSTR bstr) {
    return bstr ? std::wstring(bstr, SysStringLen(bstr)) : L"";
}

IUIAutomationElement* FindElementByNameRecursive(IUIAutomationElement* root, const std::wstring& partialName) {
    if (!root) return nullptr;

    // 获取当前节点的名称
    BSTR name = nullptr;
    if (SUCCEEDED(root->get_CurrentName(&name)) && name) {
        std::wstring currentName = BstrToWString(name);
        SysFreeString(name);

        if (currentName.find(partialName) != std::wstring::npos) {
            root->AddRef(); // 返回前 AddRef，避免提前释放
            return root;
        }
    }

    // 获取子节点
    IUIAutomationTreeWalker* walker = nullptr;
    IUIAutomationElement* child = nullptr;

    if (SUCCEEDED(g_pAutomation->get_ControlViewWalker(&walker)) && walker) {
        walker->GetFirstChildElement(root, &child);
        while (child) {
            IUIAutomationElement* found = FindElementByNameRecursive(child, partialName);
            if (found) {
                child->Release();
                walker->Release();
                return found;
            }

            IUIAutomationElement* next = nullptr;
            walker->GetNextSiblingElement(child, &next);
            child->Release();
            child = next;
        }
        walker->Release();
    }

    return nullptr;
}

HWND FindWindowByPartialTitle(const std::wstring& partialTitle) {
    struct MatchData {
        std::wstring target;
        HWND result = nullptr;
    } data{ partialTitle, nullptr };

    EnumWindows([](HWND hwnd, LPARAM lParam) -> BOOL {
        MatchData* pData = reinterpret_cast<MatchData*>(lParam);
        wchar_t title[512];
        GetWindowTextW(hwnd, title, sizeof(title) / sizeof(wchar_t));
        if (wcsstr(title, pData->target.c_str()) != nullptr) {
            pData->result = hwnd;
            return FALSE; // Stop enumeration
        }
        return TRUE; // Continue
        }, reinterpret_cast<LPARAM>(&data));

    return data.result;
}

void ClearEditBySelectAllDelete() {
    INPUT inputs[4] = {};

    // Press CTRL down
    inputs[0].type = INPUT_KEYBOARD;
    inputs[0].ki.wVk = VK_CONTROL;

    // Press A down
    inputs[1].type = INPUT_KEYBOARD;
    inputs[1].ki.wVk = 'A';

    // Release A
    inputs[2].type = INPUT_KEYBOARD;
    inputs[2].ki.wVk = 'A';
    inputs[2].ki.dwFlags = KEYEVENTF_KEYUP;

    // Release CTRL
    inputs[3].type = INPUT_KEYBOARD;
    inputs[3].ki.wVk = VK_CONTROL;
    inputs[3].ki.dwFlags = KEYEVENTF_KEYUP;

    SendInput(4, inputs, sizeof(INPUT));
    Sleep(100);

    // Press Delete
    INPUT del = {};
    del.type = INPUT_KEYBOARD;
    del.ki.wVk = VK_DELETE;
    SendInput(1, &del, sizeof(INPUT));
    Sleep(30);
    del.ki.dwFlags = KEYEVENTF_KEYUP;
    SendInput(1, &del, sizeof(INPUT));
    Sleep(100);
}

void FocusAndType(IAccessible* pEdit, const std::wstring& text) {
    if (!pEdit) return;

    VARIANT varSelf;
    VariantInit(&varSelf);
    varSelf.vt = VT_I4;
    varSelf.lVal = CHILDID_SELF;
    Sleep(1000);
    pEdit->accSelect(SELFLAG_TAKEFOCUS, varSelf);
    Sleep(300);

    // 获取父窗口
    HWND hwnd = nullptr;
    HWND* phwnd = reinterpret_cast<HWND*>(&hwnd);
    WindowFromAccessibleObject(pEdit, phwnd);
    SetForegroundWindow(hwnd);
    Sleep(100);

    ClearEditBySelectAllDelete();

    for (wchar_t ch : text) {
        SHORT vk = VkKeyScanW(ch);
        if (vk == -1) continue;

        BYTE vkCode = LOBYTE(vk);
        BYTE shiftState = HIBYTE(vk);

        std::vector<INPUT> inputs;

        if (shiftState & 1) {  // SHIFT
            INPUT shiftDown = { INPUT_KEYBOARD };
            shiftDown.ki.wVk = VK_SHIFT;
            inputs.push_back(shiftDown);
        }

        // 按键本身
        INPUT keyDown = { INPUT_KEYBOARD };
        keyDown.ki.wVk = vkCode;
        inputs.push_back(keyDown);

        INPUT keyUp = keyDown;
        keyUp.ki.dwFlags = KEYEVENTF_KEYUP;
        inputs.push_back(keyUp);

        if (shiftState & 1) {
            INPUT shiftUp = { INPUT_KEYBOARD };
            shiftUp.ki.wVk = VK_SHIFT;
            shiftUp.ki.dwFlags = KEYEVENTF_KEYUP;
            inputs.push_back(shiftUp);
        }

        SendInput(static_cast<UINT>(inputs.size()), inputs.data(), sizeof(INPUT));
    }
}


int SendChatText(const std::wstring& windowTitle, const std::wstring& input) {
    HWND hwnd = FindWindowByPartialTitle(windowTitle.c_str());
    if (!hwnd) {
        return -1;
    }

    CoCreateInstance(__uuidof(CUIAutomation), NULL, CLSCTX_INPROC_SERVER,
        IID_PPV_ARGS(&g_pAutomation));

    IUIAutomationElement* pUIRoot = nullptr;
    if (SUCCEEDED(g_pAutomation->ElementFromHandle(hwnd, &pUIRoot)) && pUIRoot) {
        IUIAutomationElement* pTarget = FindElementByNameRecursive(pUIRoot, kTargetControlName);

        VARIANT patternAvailable;
        CComPtr<IUIAutomationValuePattern> valuePattern;
        HRESULT hr = pTarget->GetCurrentPatternAs(UIA_ValuePatternId, IID_PPV_ARGS(&valuePattern));
        if (SUCCEEDED(hr) && valuePattern != nullptr) {
            BSTR bstrText = SysAllocString(L"Hello from UIAutomation!");
            valuePattern->SetValue(bstrText);
            SysFreeString(bstrText);
        }

        if (pTarget) {
            pTarget->Release();
        }
    }
    if (g_pAutomation) {
        g_pAutomation->Release();
    }

    IAccessible* pRoot = nullptr;
    HRESULT hr = AccessibleObjectFromWindow(hwnd, OBJID_CLIENT, IID_IAccessible, (void**)&pRoot);
    if (FAILED(hr) || !pRoot) {
        return -1;
    }

    IAccessible* pTargetEdit = FindAccessibleByNameAndRole(pRoot, kTargetControlName, ROLE_SYSTEM_TEXT);
    if (pTargetEdit) {
        VARIANT varSelf;
        VariantInit(&varSelf);
        varSelf.vt = VT_I4;
        varSelf.lVal = CHILDID_SELF;
        
        pTargetEdit->accSelect(SELFLAG_TAKEFOCUS, varSelf);
        HRESULT hr = pTargetEdit->put_accValue(varSelf, SysAllocString(input.c_str()));
        if (SUCCEEDED(hr)) {
            std::wcout << "succ" << std::endl;
        }
        FocusAndType(pTargetEdit, input);
        pTargetEdit->Release();
    }
    else {
        return -1;
    }

    

    IAccessible* pTargetButton = FindAccessibleByNameAndRole(pRoot, kTargetButtonName, ROLE_SYSTEM_PUSHBUTTON);
    if (!pTargetButton) {
        return -1;
    }

    ClickButton(pTargetButton);
    pTargetButton->Release();

    pRoot->Release();

    return 0;
}

int FindAndClick(const std::wstring& windowTitle, const std::wstring& btn) {
    HWND hwnd = FindWindowByPartialTitle(windowTitle.c_str());
    if (!hwnd) {
        return -1;
    }

    IAccessible* pRoot = nullptr;
    HRESULT hr = AccessibleObjectFromWindow(hwnd, OBJID_CLIENT, IID_IAccessible, (void**)&pRoot);
    if (FAILED(hr) || !pRoot) {
        return -1;
    }

    IAccessible* pTargetButton = FindAccessibleByNameAndRole(pRoot, btn, ROLE_SYSTEM_PUSHBUTTON);
    if (!pTargetButton) {
        return -1;
    }

    ClickButton(pTargetButton);
    pTargetButton->Release();

    pRoot->Release();

    return 0;

}


int WINAPI WinMain(HINSTANCE hInstance, HINSTANCE hPrevInstance, LPSTR lpCmdLine, int nCmdShow) {
    int result = -1;
    CoInitialize(nullptr);
    int argc = 0;
    LPWSTR* argv = CommandLineToArgvW(GetCommandLineW(), &argc);

    if (!argv || argc < 2) {
        MessageBoxW(nullptr, L"usage: i11y.exe Command Param1 Param2 ...", L"Error", MB_ICONWARNING);
        return result;
    }

    std::wstring cmd = argv[1];
    if (cmd == L"chat") {
        std::wstring windowTitlePart = argv[2];
        std::wstring inputText = argv[3];
        result = SendChatText(windowTitlePart, inputText);
    }
    else if (cmd == L"new") {
        std::wstring windowTitlePart = argv[2];
        result = FindAndClick(windowTitlePart, kNewButtonName);
    }
    else if (cmd == L"cancel") {
        std::wstring windowTitlePart = argv[2];
        result = FindAndClick(windowTitlePart, kCancelButtonName);
    }
    else if (cmd == L"click") {
        std::wstring windowTitlePart = argv[2];
        std::wstring btn = argv[3];
        result = FindAndClick(windowTitlePart, btn);
    }
    
    
    CoUninitialize();
    return result;
}
